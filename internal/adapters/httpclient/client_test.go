package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestClientPreservesDispatchSnapshotAcrossReconstructionAfterUnknownResponse(t *testing.T) {
	for _, test := range []struct {
		name      string
		dropFirst bool
	}{
		{"accepted control", false},
		{"received then disconnected", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			type receivedRequest struct {
				attempt int
				method  string
				path    string
				headers http.Header
				body    string
			}
			received := make(chan receivedRequest, 4)
			count := make(chan int, 1)
			count <- 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				attempt := <-count + 1
				count <- attempt
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("server reading attempt %d: %v", attempt, err)
					http.Error(writer, "read failed", http.StatusBadRequest)
					return
				}
				received <- receivedRequest{attempt, request.Method, request.URL.Path, request.Header.Clone(), string(body)}
				if test.dropFirst && attempt == 1 {
					connection, _, err := writer.(http.Hijacker).Hijack()
					if err != nil {
						t.Errorf("server hijacking first received request: %v", err)
						return
					}
					if err := connection.Close(); err != nil {
						t.Errorf("server closing first connection: %v", err)
					}
					return
				}
				writer.WriteHeader(http.StatusAccepted)
			}))
			defer server.Close()

			newClient := func() *Client {
				transport := &http.Transport{DisableKeepAlives: true}
				t.Cleanup(transport.CloseIdleConnections)
				return NewClientWithResolver(&http.Client{Transport: transport, Timeout: 2 * time.Second}, "loopback-fixture-token", nil, AddressAllowlistFunc(func(host string, address netip.Addr) bool {
					return host == "127.0.0.1" && address == netip.MustParseAddr("127.0.0.1")
				}))
			}
			work := testWork()
			work.TargetURL = server.URL + "/command"
			work.Payload = []byte("{ \"tenant_id\": \"tenant-a\", \"command\": \"nightly\", \"data\": [1, true] }\n")
			work.AttemptCount = 1
			work.Status = domain.OutboxDispatching
			identity := domain.OccurrenceIdentity(work.ScheduleSnapshot(), work.ScheduledAt)
			firstClient := newClient()
			first := firstClient.Dispatch(context.Background(), work)
			if test.dropFirst {
				if first.Status != 0 || first.Code != domain.CodeTargetUnavailable || !errors.Is(first.Err, io.EOF) || !domain.RetryableDispatch(first) {
					t.Fatalf("first response after actual server receive and disconnect = %#v, want retryable unknown EOF", first)
				}
			} else if !first.Succeeded() || first.Status != http.StatusAccepted {
				t.Fatalf("accepted control = %#v", first)
			}
			if first.RequestID != identity.RequestID || first.IdempotencyKey != identity.IdempotencyKey || first.TraceID != identity.TraceID {
				t.Fatalf("first result changed snapshot identity: %#v", first)
			}

			wantRequests := 1
			if test.dropFirst {
				reconstructed := work
				reconstructed.Payload = append([]byte(nil), work.Payload...)
				reconstructed.ScheduledAt = work.ScheduledAt.UTC()
				reconstructed.AttemptCount = 2
				reconstructed.NextAttemptAt = work.ScheduledAt.Add(time.Second)
				secondClient := newClient()
				if secondClient == firstClient || secondClient.httpClient == firstClient.httpClient {
					t.Fatal("retry must reconstruct both Scheduler Client and underlying HTTP client")
				}
				second := secondClient.Dispatch(context.Background(), reconstructed)
				if !second.Succeeded() || second.Status != http.StatusAccepted || domain.RetryableDispatch(second) {
					t.Fatalf("second response = %#v, want successful HTTP 202", second)
				}
				if second.RequestID != first.RequestID || second.IdempotencyKey != first.IdempotencyKey || second.TraceID != first.TraceID {
					t.Fatalf("reconstructed client changed identity: first=%#v second=%#v", first, second)
				}
				wantRequests = 2
			}
			server.Close()
			if got := <-count; got != wantRequests || len(received) != wantRequests {
				t.Fatalf("actual server receives = %d, captured = %d, want exactly %d", got, len(received), wantRequests)
			}
			for attempt := 1; attempt <= wantRequests; attempt++ {
				got := <-received
				if got.attempt != attempt || got.method != http.MethodPost || got.path != "/command" || got.body != string(work.Payload) {
					t.Fatalf("received attempt %d changed method/path/payload bytes: %#v", attempt, got)
				}
				for header, want := range map[string]string{
					RequestIDHeader:   identity.RequestID,
					IdempotencyHeader: identity.IdempotencyKey,
					OccurrenceHeader:  "2026-01-02T08:04:05.123Z",
					TenantHeader:      "tenant-a",
					ScheduleHeader:    "billing.reconcile",
					"Content-Type":    "application/json",
					"Authorization":   "Bearer loopback-fixture-token",
					"traceparent":     traceparent(identity.TraceID, identity.RequestID),
				} {
					if value := got.headers.Get(header); value != want {
						t.Errorf("attempt %d header %s = %q, want %q", attempt, header, value, want)
					}
				}
			}
			t.Logf("actual loopback receives=%d; owned server closed; no durable receipt or business-effect assertion", wantRequests)
		})
	}
}
