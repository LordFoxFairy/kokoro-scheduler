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

func TestOccurrenceIdentityPreservesNormalizedInstantAndSeparatesSnapshotDimensions(t *testing.T) {
	at := time.Date(2026, 1, 2, 8, 4, 5, 123_450_000, time.UTC)
	normalized := time.Date(2026, 1, 2, 8, 4, 5, 123_000_000, time.UTC)
	schedule := Schedule{ID: "00000000-0000-0000-0000-000000000001", TenantID: "tenant-a", Name: "nightly"}
	tenant := schedule
	tenant.TenantID = "tenant-b"
	id := schedule
	id.ID = "00000000-0000-0000-0000-000000000002"
	name := schedule
	name.Name = "nightly-renamed"
	baseline := OccurrenceIdentity(schedule, at)

	for _, test := range []struct {
		name       string
		schedule   Schedule
		at         time.Time
		normalized time.Time
		same       bool
	}{
		{"reconstructed snapshot", schedule, at, normalized, true},
		{"same instant west timezone", schedule, at.In(time.FixedZone("west", -5*60*60)), normalized, true},
		{"same instant east timezone", schedule, at.In(time.FixedZone("east", 9*60*60)), normalized, true},
		{"same millisecond lower precision", schedule, normalized.Add(time.Nanosecond), normalized, true},
		{"same millisecond upper precision", schedule, normalized.Add(time.Millisecond - time.Nanosecond), normalized, true},
		{"different tenant", tenant, at, normalized, false},
		{"different schedule ID", id, at, normalized, false},
		{"different schedule name", name, at, normalized, false},
		{"next millisecond", schedule, at.Add(time.Millisecond), normalized.Add(time.Millisecond), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := OccurrenceIdentity(test.schedule, test.at)
			if !got.ScheduledAt.Equal(test.normalized) || got.ScheduledAt.Location() != time.UTC || got.ScheduledAt.Nanosecond()%int(time.Millisecond) != 0 {
				t.Fatalf("scheduled instant = %v, want UTC %v at millisecond precision", got.ScheduledAt, test.normalized)
			}
			if got.RequestID == "" || got.IdempotencyKey == "" || got.TraceID == "" {
				t.Fatalf("empty occurrence identity: %#v", got)
			}
			if same := got.RequestID == baseline.RequestID; same != test.same {
				t.Errorf("request ID equal to baseline = %v, want %v", same, test.same)
			}
			if same := got.IdempotencyKey == baseline.IdempotencyKey; same != test.same {
				t.Errorf("idempotency key equal to baseline = %v, want %v", same, test.same)
			}
			if same := got.TraceID == baseline.TraceID; same != test.same {
				t.Errorf("trace ID equal to baseline = %v, want %v", same, test.same)
			}
		})
	}
}
