package httpclient

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

func TestClientDispatchUsesStableUTCIdentityAndTargetAuth(t *testing.T) {
	var got http.Header
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		got = request.Header.Clone()
		return &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody, Header: make(http.Header), Request: request}, nil
	})}, "target-service-token", &fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, nil)
	job := domain.Job{Name: "billing.reconcile", Schedule: "@every 1m", URL: "http://service.test/command", Method: "POST", Body: []byte(`{"tenant_id":"TENANT"}`), Retry: domain.RetryPolicy{MaxAttempts: 1}}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("fixture", -5*60*60))
	result := client.Dispatch(context.Background(), job, domain.NewOccurrence(job.Name, at, at))
	if result.Err != nil || result.Status != http.StatusAccepted {
		t.Fatalf("result = %#v", result)
	}
	if got.Get(OccurrenceHeader) != "20260102T080405Z" || got.Get(RequestIDHeader) != "sched_billing.reconcile_20260102T080405Z" || got.Get(IdempotencyHeader) != "schedule:billing.reconcile:20260102T080405Z" {
		t.Fatalf("unexpected identity headers: %#v", got)
	}
	if got.Get("Authorization") != "Bearer target-service-token" {
		t.Fatalf("authorization = %q", got.Get("Authorization"))
	}
	if result.TraceID == "" || !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`).MatchString(got.Get("traceparent")) {
		t.Fatalf("trace identity result=%q header=%q", result.TraceID, got.Get("traceparent"))
	}
}

func TestClientClassifiesTimeout(t *testing.T) {
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}, "", &fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, nil)
	job := domain.Job{Name: "slow", Schedule: "@every 1m", URL: "http://service.test/command", Method: "POST", Body: []byte(`{}`), Retry: domain.RetryPolicy{MaxAttempts: 1}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result := client.Dispatch(ctx, job, domain.NewOccurrence(job.Name, time.Now(), time.Now()))
	if result.Code != "SCHEDULER_TARGET_TIMEOUT" {
		t.Fatalf("code = %q, result = %#v", result.Code, result)
	}
}

func TestNewDefaultClientConfiguresPhaseTimeouts(t *testing.T) {
	client := NewDefaultClient(3*time.Second, "")
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("default transport = %#v, want *http.Transport", client.httpClient.Transport)
	}
	if transport.DialContext == nil {
		t.Fatal("default transport must configure a bounded dialer")
	}
	if transport.TLSHandshakeTimeout != 3*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %s, want 3s", transport.TLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout != 3*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %s, want 3s", transport.ResponseHeaderTimeout)
	}
	if transport.IdleConnTimeout <= 0 {
		t.Fatalf("IdleConnTimeout = %s, want a positive connection lifetime", transport.IdleConnTimeout)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var calls int
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		response := &http.Response{StatusCode: http.StatusFound, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: request}
		response.Header.Set("Location", "http://other.test/command")
		return response, nil
	})}, "", &fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, nil)
	job := domain.Job{Name: "redirect", Schedule: "@every 1m", URL: "http://service.test/command", Method: "POST", Body: []byte(`{}`), Retry: domain.RetryPolicy{MaxAttempts: 1}}
	result := client.Dispatch(context.Background(), job, domain.NewOccurrence(job.Name, time.Now(), time.Now()))
	if result.Status != http.StatusFound || result.Err == nil {
		t.Fatalf("redirect result = %#v, want rejected 302", result)
	}
	if calls != 1 {
		t.Fatalf("redirect round trips = %d, want one", calls)
	}
}

func TestClientRejectsResponseBodyAboveLimit(t *testing.T) {
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 1<<20+1))), Header: make(http.Header), Request: request}, nil
	})}, "", &fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, nil)
	job := domain.Job{Name: "large-response", Schedule: "@every 1m", URL: "http://service.test/command", Method: "POST", Body: []byte(`{}`), Retry: domain.RetryPolicy{MaxAttempts: 1}}
	result := client.Dispatch(context.Background(), job, domain.NewOccurrence(job.Name, time.Now(), time.Now()))
	if result.Err == nil || result.Code != "SCHEDULER_TARGET_REJECTED" {
		t.Fatalf("large response result = %#v, want rejected body limit", result)
	}
}

type fixedResolver struct {
	addresses []netip.Addr
	lookups   int
}

func (r *fixedResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	r.lookups++
	return r.addresses, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClientRejectsDNSResolvedPrivateTargetBeforeDial(t *testing.T) {
	resolver := &fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("10.0.0.1")}}
	var requests int
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		requests++
		return nil, nil
	})}, "", resolver, nil)
	job := domain.Job{Name: "private-dns", Schedule: "@every 1m", URL: "http://service.test/command", Method: "POST", Body: []byte(`{}`), Retry: domain.RetryPolicy{MaxAttempts: 1}}
	result := client.Dispatch(context.Background(), job, domain.NewOccurrence(job.Name, time.Now(), time.Now()))
	if result.Err == nil || result.Code != "SCHEDULER_TARGET_REJECTED" {
		t.Fatalf("private DNS result = %#v, want rejected", result)
	}
	if resolver.lookups != 1 || requests != 0 {
		t.Fatalf("resolver lookups=%d requests=%d, want one lookup and no request", resolver.lookups, requests)
	}
}

func TestClientAllowsExplicitlyAllowlistedPrivateTarget(t *testing.T) {
	resolver := &fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("10.0.0.7")}}
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody, Header: make(http.Header), Request: request}, nil
	})}, "", resolver, AddressAllowlistFunc(func(host string, address netip.Addr) bool {
		return host == "service.test" && address == netip.MustParseAddr("10.0.0.7")
	}))
	job := domain.Job{Name: "allowlisted-private", Schedule: "@every 1m", URL: "http://service.test/command", Method: "POST", Body: []byte(`{}`), Retry: domain.RetryPolicy{MaxAttempts: 1}}
	result := client.Dispatch(context.Background(), job, domain.NewOccurrence(job.Name, time.Now(), time.Now()))
	if result.Err != nil || result.Status != http.StatusAccepted {
		t.Fatalf("allowlisted private result = %#v, want accepted", result)
	}
}

func TestClientPinsResolvedAddressWhilePreservingHTTPHost(t *testing.T) {
	resolver := &fixedResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
	var gotURLHost, gotHost string
	client := NewClientWithResolver(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotURLHost = request.URL.Host
		gotHost = request.Host
		return &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody, Header: make(http.Header)}, nil
	})}, "", resolver, nil)
	job := domain.Job{Name: "pinned-dns", Schedule: "@every 1m", URL: "http://service.test/command", Method: "POST", Body: []byte(`{}`), Retry: domain.RetryPolicy{MaxAttempts: 1}}
	result := client.Dispatch(context.Background(), job, domain.NewOccurrence(job.Name, time.Now(), time.Now()))
	if result.Err != nil || result.Status != http.StatusAccepted {
		t.Fatalf("pinned DNS result = %#v", result)
	}
	if gotURLHost != "93.184.216.34:80" || gotHost != "service.test" {
		t.Fatalf("request URL host=%q Host=%q, want pinned address and original host", gotURLHost, gotHost)
	}
}
