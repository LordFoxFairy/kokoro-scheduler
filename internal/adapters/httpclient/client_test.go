package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

func TestClientDispatchUsesTenantScopedRFC3339IdentityAndTargetAuth(t *testing.T) {
	var got http.Header
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		got = request.Header.Clone()
		return response(request, http.StatusAccepted, ""), nil
	})}, "target-service-token", publicResolver(), nil)
	work := testWork()
	result := client.Dispatch(context.Background(), work)
	if result.Err != nil || result.Status != http.StatusAccepted {
		t.Fatalf("result = %#v", result)
	}
	identity := domain.OccurrenceIdentity(work.ScheduleSnapshot(), work.ScheduledAt)
	if got.Get(OccurrenceHeader) != "2026-01-02T08:04:05.123Z" || got.Get(RequestIDHeader) != identity.RequestID || got.Get(IdempotencyHeader) != identity.IdempotencyKey {
		t.Fatalf("unexpected identity headers: %#v", got)
	}
	if got.Get(TenantHeader) != "tenant-a" || got.Get(ScheduleHeader) != "billing.reconcile" {
		t.Fatalf("tenant/schedule headers: %#v", got)
	}
	if got.Get("Authorization") != "Bearer target-service-token" {
		t.Fatalf("authorization = %q", got.Get("Authorization"))
	}
	if result.TraceID == "" || !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`).MatchString(got.Get("traceparent")) {
		t.Fatalf("trace identity result=%q header=%q", result.TraceID, got.Get("traceparent"))
	}
}

func TestClientClassifiesTransientHTTPStatuses(t *testing.T) {
	for _, status := range []int{408, 425, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return response(request, status, ""), nil
			})}, "", publicResolver(), nil)
			result := client.Dispatch(context.Background(), testWork())
			if !domain.RetryableDispatch(result) {
				t.Fatalf("HTTP %d result = %#v, want retryable", status, result)
			}
		})
	}
}

func TestClientClassifiesOther4xxAsPermanent(t *testing.T) {
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(request, http.StatusConflict, ""), nil
	})}, "", publicResolver(), nil)
	result := client.Dispatch(context.Background(), testWork())
	if result.Code != domain.CodeTargetPermanent || domain.RetryableDispatch(result) {
		t.Fatalf("HTTP 409 result = %#v, want permanent", result)
	}
}

func TestClientClassifiesTimeout(t *testing.T) {
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}, "", publicResolver(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result := client.Dispatch(ctx, testWork())
	if result.Code != domain.CodeTargetTimeout {
		t.Fatalf("code = %q, result = %#v", result.Code, result)
	}
}

func TestClientPropagatesCallerCancellationAsPermanent(t *testing.T) {
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}, "", publicResolver(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := client.Dispatch(ctx, testWork())
	if result.Code != domain.CodeCancelled || domain.RetryableDispatch(result) {
		t.Fatalf("cancelled result = %#v, want permanent cancellation", result)
	}
}

func TestClientDoesNotExposeTargetQueryInNetworkError(t *testing.T) {
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}, "", publicResolver(), nil)
	work := testWork()
	work.TargetURL = "http://service.test/command?token=do-not-log"
	result := client.Dispatch(context.Background(), work)
	if result.Err == nil || strings.Contains(result.Err.Error(), "do-not-log") || result.Code != domain.CodeTargetUnavailable {
		t.Fatalf("network result leaked target query or was misclassified: %#v", result)
	}
}

func TestClientClassifiesResponseReadFailureAsRetryableNetworkError(t *testing.T) {
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(failingReader{}),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}, "", publicResolver(), nil)
	result := client.Dispatch(context.Background(), testWork())
	if result.Code != domain.CodeTargetUnavailable || !domain.RetryableDispatch(result) {
		t.Fatalf("response read failure = %#v, want retryable network result", result)
	}
}

func TestNewDefaultClientConfiguresPhaseTimeouts(t *testing.T) {
	client := NewDefaultClient(3*time.Second, "")
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil || transport.DialContext == nil {
		t.Fatalf("default transport = %#v", client.httpClient.Transport)
	}
	if transport.TLSHandshakeTimeout != 3*time.Second || transport.ResponseHeaderTimeout != 3*time.Second || transport.IdleConnTimeout <= 0 {
		t.Fatalf("timeouts = TLS %s response %s idle %s", transport.TLSHandshakeTimeout, transport.ResponseHeaderTimeout, transport.IdleConnTimeout)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var calls int
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		result := response(request, http.StatusFound, "")
		result.Header.Set("Location", "http://other.test/command")
		return result, nil
	})}, "", publicResolver(), nil)
	result := client.Dispatch(context.Background(), testWork())
	if result.Status != http.StatusFound || result.Code != domain.CodeTargetPermanent || calls != 1 {
		t.Fatalf("redirect result=%#v calls=%d", result, calls)
	}
}

func TestClientRejectsResponseBodyAboveLimit(t *testing.T) {
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(request, http.StatusOK, strings.Repeat("x", 1<<20+1)), nil
	})}, "", publicResolver(), nil)
	result := client.Dispatch(context.Background(), testWork())
	if result.Err == nil || result.Code != domain.CodeTargetRejected {
		t.Fatalf("large response result = %#v", result)
	}
}

func TestClientRejectsDNSResolvedPrivateTargetBeforeDial(t *testing.T) {
	resolver := &fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1")}}
	var requests int
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("must not dial")
	})}, "", resolver, nil)
	result := client.Dispatch(context.Background(), testWork())
	if result.Code != domain.CodeTargetRejected || resolver.lookups != 1 || requests != 0 {
		t.Fatalf("private result=%#v lookups=%d requests=%d", result, resolver.lookups, requests)
	}
}

func TestClientPinsAllowedAddressAndPreservesHost(t *testing.T) {
	resolver := &fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("10.0.0.7")}}
	var urlHost, host string
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		urlHost, host = request.URL.Host, request.Host
		return response(request, http.StatusAccepted, ""), nil
	})}, "", resolver, AddressAllowlistFunc(func(name string, address netip.Addr) bool {
		return name == "service.test" && address == netip.MustParseAddr("10.0.0.7")
	}))
	result := client.Dispatch(context.Background(), testWork())
	if result.Err != nil || urlHost != "10.0.0.7:80" || host != "service.test" {
		t.Fatalf("result=%#v URL host=%q Host=%q", result, urlHost, host)
	}
}

func testWork() domain.DispatchWork {
	return domain.DispatchWork{
		ID: "00000000-0000-0000-0000-000000000003", TenantID: "tenant-a",
		OccurrenceID: "00000000-0000-0000-0000-000000000002",
		ScheduleID:   "00000000-0000-0000-0000-000000000001", ScheduleName: "billing.reconcile",
		ScheduledAt: time.Date(2026, 1, 2, 3, 4, 5, 123_000_000, time.FixedZone("fixture", -5*60*60)),
		TargetURL:   "http://service.test/command", Method: domain.MethodPost,
		Payload: []byte(`{"tenant_id":"tenant-a"}`), Retry: domain.DefaultRetryPolicy(),
	}
}

func publicResolver() *fixedResolver {
	return &fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
}

func response(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}
}

type fixedResolver struct {
	addresses []netip.Addr
	lookups   int
}

func (r *fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.lookups++
	return r.addresses, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
