package domain

import (
	"errors"
	"testing"
	"time"
)

func TestScheduleNormalizedUsesDurableRecoveryDefaults(t *testing.T) {
	schedule, err := (Schedule{
		TenantID:  "tenant-a",
		Name:      "billing.reconcile",
		Rule:      "0 2 * * *",
		TargetURL: "http://service.test/command",
	}).Normalized()
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Timezone != "UTC" {
		t.Fatalf("timezone = %q, want UTC", schedule.Timezone)
	}
	if schedule.MisfirePolicy != MisfireFireOnce || schedule.CatchUpLimit != 1 {
		t.Fatalf("misfire defaults = %q/%d, want fire_once/1", schedule.MisfirePolicy, schedule.CatchUpLimit)
	}
	if schedule.OverlapPolicy != OverlapForbid {
		t.Fatalf("overlap default = %q, want forbid", schedule.OverlapPolicy)
	}
	if schedule.Status != ScheduleActive {
		t.Fatalf("status default = %q, want active", schedule.Status)
	}
}

func TestScheduleValidatesIANAZoneAndBoundedCatchUp(t *testing.T) {
	valid := Schedule{
		TenantID:      "tenant-a",
		Name:          "nightly",
		Rule:          "30 1 * * *",
		Timezone:      "America/New_York",
		TargetURL:     "http://service.test/command",
		MisfirePolicy: MisfireCatchUpBounded,
		CatchUpLimit:  4,
	}
	if _, err := valid.Normalized(); err != nil {
		t.Fatalf("valid schedule: %v", err)
	}

	invalidZone := valid
	invalidZone.Timezone = "EST"
	if _, err := invalidZone.Normalized(); err == nil {
		t.Fatal("timezone abbreviations must not replace an explicit IANA timezone")
	}

	invalidBound := valid
	invalidBound.CatchUpLimit = MaxCatchUpOccurrences + 1
	if _, err := invalidBound.Normalized(); err == nil {
		t.Fatal("catch-up limits above the durable bound must be rejected")
	}
}

func TestOccurrenceIdentityIncludesTenantAndSchedule(t *testing.T) {
	at := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	left := Schedule{ID: "00000000-0000-0000-0000-000000000001", TenantID: "tenant-a", Name: "nightly"}
	right := left
	right.TenantID = "tenant-b"
	if OccurrenceIdentity(left, at).IdempotencyKey == OccurrenceIdentity(right, at).IdempotencyKey {
		t.Fatal("different tenants must not share an occurrence idempotency key")
	}
	if got := OccurrenceIdentity(left, at).ScheduledAt; got.Location() != time.UTC || got.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatalf("scheduled instant = %v, want UTC millisecond precision", got)
	}
}

func TestDispatchResultClassifiesTransientAndPermanentFailures(t *testing.T) {
	for _, result := range []DispatchResult{
		{Status: 408, Err: errors.New("request timeout")},
		{Status: 425, Err: errors.New("too early")},
		{Status: 429, Err: errors.New("rate limited")},
		{Status: 500, Err: errors.New("server error")},
		{Status: 0, Err: errors.New("network error"), Code: CodeTargetUnavailable},
		{Status: 200, Err: errors.New("response body read failed"), Code: CodeTargetUnavailable},
	} {
		if !RetryableDispatch(result) {
			t.Errorf("result %#v must be retryable", result)
		}
	}
	for _, result := range []DispatchResult{
		{Status: 400, Err: errors.New("bad request")},
		{Status: 409, Err: errors.New("conflict")},
		{Status: 0, Err: errors.New("policy rejected"), Code: CodeTargetRejected},
		{Status: 0, Err: errors.New("caller cancelled"), Code: CodeCancelled},
	} {
		if RetryableDispatch(result) {
			t.Errorf("result %#v must be permanent", result)
		}
	}
}
