package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

func TestClientDispatchUsesStableUTCIdentityAndTargetAuth(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := NewDefaultClient(time.Second, "target-service-token")
	job := domain.Job{Name: "billing.reconcile", Schedule: "@every 1m", URL: server.URL, Method: "POST", Body: []byte(`{"tenant_id":"TENANT"}`), Retry: domain.RetryPolicy{MaxAttempts: 1}}
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	client := NewClient(&http.Client{}, "")
	job := domain.Job{Name: "slow", Schedule: "@every 1m", URL: server.URL, Method: "POST", Body: []byte(`{}`), Retry: domain.RetryPolicy{MaxAttempts: 1}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result := client.Dispatch(ctx, job, domain.NewOccurrence(job.Name, time.Now(), time.Now()))
	if result.Code != "SCHEDULER_TARGET_TIMEOUT" {
		t.Fatalf("code = %q, result = %#v", result.Code, result)
	}
}
