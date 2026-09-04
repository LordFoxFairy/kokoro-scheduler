package application

import (
	"context"
	"testing"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

func TestNormalizeAttemptResultClassifiesCallerCancellationAsPermanent(t *testing.T) {
	result := normalizeAttemptResult(domain.DispatchResult{Err: context.Canceled}, context.Canceled)
	if result.Code != domain.CodeCancelled || domain.RetryableDispatch(result) {
		t.Fatalf("cancelled result = %#v, want permanent cancellation", result)
	}
}

func TestNormalizeAttemptResultClassifiesDeadlineAsRetryableTimeout(t *testing.T) {
	result := normalizeAttemptResult(domain.DispatchResult{Err: context.DeadlineExceeded}, context.DeadlineExceeded)
	if result.Code != domain.CodeTargetTimeout || !domain.RetryableDispatch(result) {
		t.Fatalf("deadline result = %#v, want retryable timeout", result)
	}
}

func TestRetryBackoffDoublesAndCapsAtConfiguredMaximum(t *testing.T) {
	policy := domain.RetryPolicy{BackoffSeconds: 2, MaxBackoffSeconds: 10}
	for _, test := range []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 2 * time.Second},
		{attempt: 2, want: 4 * time.Second},
		{attempt: 3, want: 8 * time.Second},
		{attempt: 4, want: 10 * time.Second},
		{attempt: 10, want: 10 * time.Second},
	} {
		if got := retryBackoff(policy, test.attempt); got != test.want {
			t.Errorf("attempt %d backoff = %s, want %s", test.attempt, got, test.want)
		}
	}
}

func TestFullJitterUsesBoundedRandomSource(t *testing.T) {
	random := &fixedRandom{value: int64(750 * time.Millisecond)}
	dispatcher := &Dispatcher{random: random}
	delay, err := dispatcher.fullJitter(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if delay != 750*time.Millisecond || random.maximum != int64(time.Second)+1 {
		t.Fatalf("delay=%s maximum=%d", delay, random.maximum)
	}
}

type fixedRandom struct {
	value   int64
	maximum int64
}

func (r *fixedRandom) Int63n(maximum int64) (int64, error) {
	r.maximum = maximum
	return r.value, nil
}
