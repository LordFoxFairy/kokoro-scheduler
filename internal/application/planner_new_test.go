package application

import (
	"testing"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

type minuteRecurrence struct{}

func (minuteRecurrence) Validate(string, string) error { return nil }
func (minuteRecurrence) Next(_ string, _ string, after time.Time) (time.Time, error) {
	return after.Add(time.Minute), nil
}
func (minuteRecurrence) NextAfter(_ string, _ string, anchor, threshold time.Time) (time.Time, error) {
	if anchor.After(threshold) {
		return anchor, nil
	}
	steps := threshold.Sub(anchor)/time.Minute + 1
	return anchor.Add(steps * time.Minute), nil
}

func scheduleDueAt(at time.Time, policy domain.MisfirePolicy, limit int) domain.Schedule {
	return domain.Schedule{
		ID: "00000000-0000-0000-0000-000000000001", TenantID: "tenant-a", Name: "job",
		Rule: "@every 1m", Timezone: "UTC", TargetURL: "http://service.test/command",
		Method: domain.MethodPost, Payload: []byte(`{}`), Retry: domain.DefaultRetryPolicy(),
		MisfirePolicy: policy, CatchUpLimit: limit, OverlapPolicy: domain.OverlapForbid,
		Status: domain.ScheduleActive, NextDueAt: at,
	}
}

func TestPlanScheduleFireOnceRecoversExactlyOneMissedOccurrence(t *testing.T) {
	due := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := due.Add(5 * time.Minute)
	plans, next, err := planSchedule(scheduleDueAt(due, domain.MisfireFireOnce, 1), now, minuteRecurrence{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || !plans[0].Dispatch || !plans[0].ScheduledAt.Equal(due) {
		t.Fatalf("plans = %#v, want one recovered dispatch", plans)
	}
	if want := now.Add(time.Minute); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestPlanScheduleSkipPersistsAnObservableSkippedOccurrence(t *testing.T) {
	due := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := due.Add(5 * time.Minute)
	plans, _, err := planSchedule(scheduleDueAt(due, domain.MisfireSkip, 1), now, minuteRecurrence{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Dispatch || plans[0].Status != domain.OccurrenceSkipped || plans[0].OutcomeCode != domain.CodeMisfireSkipped {
		t.Fatalf("plans = %#v, want persisted misfire skip", plans)
	}
}

func TestPlanScheduleBoundsCatchUpAndRecordsDroppedRemainder(t *testing.T) {
	due := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := due.Add(5 * time.Minute)
	plans, next, err := planSchedule(scheduleDueAt(due, domain.MisfireCatchUpBounded, 2), now, minuteRecurrence{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 3 || !plans[0].Dispatch || !plans[1].Dispatch {
		t.Fatalf("plans = %#v, want two catch-up dispatches and one bound marker", plans)
	}
	last := plans[2]
	if last.Dispatch || last.OutcomeCode != domain.CodeMisfireBoundExceeded || last.Status != domain.OccurrenceSkipped {
		t.Fatalf("bound marker = %#v", last)
	}
	if want := now.Add(time.Minute); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}
