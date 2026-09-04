package httpclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

const (
	OccurrenceHeader           = "X-Kokoro-Scheduler-Occurrence"
	ScheduleHeader             = "X-Kokoro-Scheduler-Schedule"
	TenantHeader               = "X-Kokoro-Tenant-Id"
	RequestIDHeader            = "X-Request-Id"
	IdempotencyHeader          = "Idempotency-Key"
	MaxResponseBodyBytes int64 = 1 << 20
)

// Resolver is deliberately narrower than net.Resolver so tests and local
// service.test fixtures can provide deterministic answers. A resolved address
// is pinned into the request before the HTTP transport is called, avoiding a
// validation-then-redial DNS race.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type AddressAllowlist interface {
	Allows(host string, address netip.Addr) bool
}

type AddressAllowlistFunc func(host string, address netip.Addr) bool

func (f AddressAllowlistFunc) Allows(host string, address netip.Addr) bool {
	return f(host, address)
}

var errTargetRejected = errors.New("target rejected by outbound policy")

type Client struct {
	httpClient         *http.Client
	targetServiceToken string
	resolver           Resolver
	allowlist          AddressAllowlist
}

func NewClient(httpClient *http.Client, targetServiceToken string) *Client {
	return NewClientWithResolver(httpClient, targetServiceToken, net.DefaultResolver, nil)
}

func NewClientWithResolver(httpClient *http.Client, targetServiceToken string, resolver Resolver, allowlist AddressAllowlist) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &Client{httpClient: &clientCopy, targetServiceToken: strings.TrimSpace(targetServiceToken), resolver: resolver, allowlist: allowlist}
}

func NewDefaultClient(timeout time.Duration, targetServiceToken string) *Client {
	return NewDefaultClientWithAllowlist(timeout, targetServiceToken, nil)
}

func NewDefaultClientWithAllowlist(timeout time.Duration, targetServiceToken string, allowlist AddressAllowlist) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return NewClientWithResolver(&http.Client{Transport: transport, Timeout: timeout}, targetServiceToken, net.DefaultResolver, allowlist)
}

func (c *Client) Dispatch(ctx context.Context, work domain.DispatchWork) domain.DispatchResult {
	identity := domain.OccurrenceIdentity(work.ScheduleSnapshot(), work.ScheduledAt)
	result := domain.DispatchResult{
		RequestID:      identity.RequestID,
		IdempotencyKey: identity.IdempotencyKey,
		TraceID:        identity.TraceID,
	}
	requestCtx := ctx
	cancel := func() {}
	if c.httpClient.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, c.httpClient.Timeout)
	}
	defer cancel()
	body := work.Payload
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	if !json.Valid(body) {
		result.Err = errors.New("dispatch payload is invalid JSON")
		result.Code = domain.CodeTargetRejected
		return result
	}
	parsedURL, targetAddress, err := c.resolveTarget(requestCtx, work.TargetURL)
	if err != nil {
		result.Code = classifyTransportError(requestCtx, err)
		result.Err = err
		return result
	}
	request, err := http.NewRequestWithContext(requestCtx, string(work.Method), work.TargetURL, bytes.NewReader(body))
	if err != nil {
		result.Err = err
		result.Code = domain.CodeTargetRejected
		return result
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(TenantHeader, work.TenantID)
	request.Header.Set(ScheduleHeader, work.ScheduleName)
	request.Header.Set(OccurrenceHeader, identity.ScheduledAt.Format(time.RFC3339Nano))
	request.Header.Set(RequestIDHeader, identity.RequestID)
	request.Header.Set(IdempotencyHeader, identity.IdempotencyKey)
	request.Header.Set("traceparent", traceparent(identity.TraceID, identity.RequestID))
	if c.targetServiceToken != "" {
		request.Header.Set("Authorization", "Bearer "+c.targetServiceToken)
	}

	request.Host = parsedURL.Host
	request.URL.Host = net.JoinHostPort(targetAddress.String(), parsedURL.Port())
	if parsedURL.Port() == "" {
		request.URL.Host = net.JoinHostPort(targetAddress.String(), defaultPort(parsedURL.Scheme))
	}
	response, err := c.doResolved(request, parsedURL.Hostname())
	if err != nil {
		result.Code = classifyTransportError(requestCtx, err)
		result.Err = sanitizeTransportError(err)
		return result
	}
	defer response.Body.Close()
	result.Status = response.StatusCode
	if response.ContentLength > MaxResponseBodyBytes {
		result.Code = domain.CodeTargetRejected
		result.Err = fmt.Errorf("target response body exceeds %d bytes", MaxResponseBodyBytes)
		return result
	}
	bytesRead, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, MaxResponseBodyBytes+1))
	if readErr != nil {
		result.Code = domain.CodeTargetUnavailable
		result.Err = readErr
		return result
	}
	if bytesRead > MaxResponseBodyBytes {
		result.Code = domain.CodeTargetRejected
		result.Err = fmt.Errorf("target response body exceeds %d bytes", MaxResponseBodyBytes)
		return result
	}
	if !result.Succeeded() {
		if response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooEarly || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
			result.Code = domain.CodeTargetUnavailable
		} else {
			result.Code = domain.CodeTargetPermanent
		}
		result.Err = fmt.Errorf("target returned HTTP %d", response.StatusCode)
	}
	return result
}

func classifyTransportError(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return domain.CodeCancelled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return domain.CodeTargetTimeout
	}
	if errors.Is(err, errTargetRejected) {
		return domain.CodeTargetRejected
	}
	return domain.CodeTargetUnavailable
}

func sanitizeTransportError(err error) error {
	var requestError *url.Error
	if errors.As(err, &requestError) && requestError.Err != nil {
		return fmt.Errorf("dispatch target request: %w", requestError.Err)
	}
	return err
}

func (c *Client) resolveTarget(ctx context.Context, rawURL string) (*url.URL, netip.Addr, error) {
	if err := domain.ValidateTargetURLSyntax(rawURL); err != nil {
		return nil, netip.Addr{}, fmt.Errorf("%w: %v", errTargetRejected, err)
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, netip.Addr{}, err
	}
	hostname := parsedURL.Hostname()
	addresses := make([]netip.Addr, 0, 1)
	if literal, parseErr := netip.ParseAddr(hostname); parseErr == nil {
		addresses = append(addresses, literal)
	} else {
		addresses, err = c.resolver.LookupNetIP(ctx, "ip", hostname)
		if err != nil {
			return nil, netip.Addr{}, fmt.Errorf("resolve target host: %w", err)
		}
	}
	for _, address := range addresses {
		address = address.Unmap()
		if c.targetAddressAllowed(hostname, address) {
			return parsedURL, address, nil
		}
	}
	return nil, netip.Addr{}, fmt.Errorf("%w: target resolved to a disallowed IP address", errTargetRejected)
}

func (c *Client) targetAddressAllowed(hostname string, address netip.Addr) bool {
	if c.allowlist != nil {
		return c.allowlist.Allows(hostname, address)
	}
	return domain.IsSafeTargetAddress(address)
}

func (c *Client) doResolved(request *http.Request, serverName string) (*http.Response, error) {
	clientCopy := *c.httpClient
	transport := c.httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if baseTransport, ok := transport.(*http.Transport); ok {
		transportCopy := baseTransport.Clone()
		transportCopy.Proxy = nil
		if request.URL.Scheme == "https" {
			tlsConfig := transportCopy.TLSClientConfig
			if tlsConfig == nil {
				tlsConfig = &tls.Config{}
			} else {
				tlsConfig = tlsConfig.Clone()
			}
			tlsConfig.ServerName = serverName
			transportCopy.TLSClientConfig = tlsConfig
		}
		clientCopy.Transport = transportCopy
	}
	return clientCopy.Do(request)
}

func defaultPort(scheme string) string {
	if strings.EqualFold(scheme, "https") {
		return "443"
	}
	return "80"
}

func traceparent(traceID, requestID string) string {
	digest := sha256.Sum256([]byte(requestID + ":dispatch"))
	spanID := hex.EncodeToString(digest[:8])
	return "00-" + traceID + "-" + spanID + "-01"
}
