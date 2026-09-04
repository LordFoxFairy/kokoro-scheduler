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
	JobHeader                  = "X-Kokoro-Scheduler-Job"
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

func (c *Client) Dispatch(ctx context.Context, job domain.Job, occurrence domain.Occurrence) domain.RunResult {
	requestID, idempotencyKey := domain.RequestIdentity(job, occurrence.ScheduledAt)
	traceID := domain.TraceIdentity(job, occurrence.ScheduledAt)
	requestCtx := ctx
	cancel := func() {}
	if c.httpClient.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, c.httpClient.Timeout)
	}
	defer cancel()
	body := job.Body
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	if !json.Valid(body) {
		return domain.RunResult{Err: errors.New("job body is invalid JSON"), Code: "SCHEDULER_TARGET_UNAVAILABLE", RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
	}
	parsedURL, targetAddress, err := c.resolveTarget(requestCtx, job.URL)
	if err != nil {
		code := "SCHEDULER_TARGET_REJECTED"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			code = "SCHEDULER_TARGET_TIMEOUT"
		} else if !errors.Is(err, errTargetRejected) {
			code = "SCHEDULER_TARGET_UNAVAILABLE"
		}
		return domain.RunResult{Err: err, Code: code, RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
	}
	request, err := http.NewRequestWithContext(requestCtx, string(job.Method), job.URL, bytes.NewReader(body))
	if err != nil {
		return domain.RunResult{Err: err, Code: "SCHEDULER_TARGET_UNAVAILABLE", RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(JobHeader, job.Name)
	request.Header.Set(OccurrenceHeader, occurrence.Identity())
	request.Header.Set(RequestIDHeader, requestID)
	request.Header.Set(IdempotencyHeader, idempotencyKey)
	request.Header.Set("traceparent", traceparent(traceID, requestID))
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
		code := "SCHEDULER_TARGET_UNAVAILABLE"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			code = "SCHEDULER_TARGET_TIMEOUT"
		}
		return domain.RunResult{Err: err, Code: code, RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
	}
	defer response.Body.Close()
	result := domain.RunResult{Status: response.StatusCode, RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
	if response.ContentLength > MaxResponseBodyBytes {
		result.Code = "SCHEDULER_TARGET_REJECTED"
		result.Err = fmt.Errorf("target response body exceeds %d bytes", MaxResponseBodyBytes)
		return result
	}
	bytesRead, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, MaxResponseBodyBytes+1))
	if readErr != nil {
		result.Code = "SCHEDULER_TARGET_UNAVAILABLE"
		result.Err = readErr
		return result
	}
	if bytesRead > MaxResponseBodyBytes {
		result.Code = "SCHEDULER_TARGET_REJECTED"
		result.Err = fmt.Errorf("target response body exceeds %d bytes", MaxResponseBodyBytes)
		return result
	}
	if !result.Succeeded() {
		result.Code = "SCHEDULER_TARGET_REJECTED"
		result.Err = fmt.Errorf("job returned HTTP %d", response.StatusCode)
	}
	return result
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
