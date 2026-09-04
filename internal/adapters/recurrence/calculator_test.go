package recurrence

import (
	"testing"
	"time"
)

func TestCalculatorSkipsMissingSpringForwardLocalTime(t *testing.T) {
	calculator := NewCalculator()
	after := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)
	next, err := calculator.Next("30 2 * * *", "America/New_York", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestCalculatorReturnsBothFallBackLocalInstants(t *testing.T) {
	calculator := NewCalculator()
	beforeFirst := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	first, err := calculator.Next("30 1 * * *", "America/New_York", beforeFirst)
	if err != nil {
		t.Fatal(err)
	}
	second, err := calculator.Next("30 1 * * *", "America/New_York", first)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC); !first.Equal(want) {
		t.Fatalf("first fallback occurrence = %s, want %s", first, want)
	}
	if want := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC); !second.Equal(want) {
		t.Fatalf("second fallback occurrence = %s, want %s", second, want)
	}
}

func TestCalculatorPreservesEveryAnchorWhenAdvancingPastDowntime(t *testing.T) {
	calculator := NewCalculator()
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	threshold := anchor.Add(95 * time.Second)
	next, err := calculator.NextAfter("@every 30s", "UTC", anchor, threshold)
	if err != nil {
		t.Fatal(err)
	}
	if want := anchor.Add(120 * time.Second); !next.Equal(want) {
		t.Fatalf("next = %s, want anchored %s", next, want)
	}
}

func TestCalculatorRejectsTimezonePrefixesInRule(t *testing.T) {
	calculator := NewCalculator()
	if err := calculator.Validate("CRON_TZ=UTC 0 * * * *", "UTC"); err == nil {
		t.Fatal("timezone must have one canonical field, not an embedded cron prefix")
	}
}

func TestCalculatorRejectsImpossibleCalendarDate(t *testing.T) {
	calculator := NewCalculator()
	if err := calculator.Validate("0 0 31 FEB *", "UTC"); err == nil {
		t.Fatal("31 February must be rejected before a schedule is persisted")
	}
	if err := calculator.Validate("0 0 29 FEB *", "UTC"); err != nil {
		t.Fatalf("leap-day schedule must remain valid: %v", err)
	}
}

func TestCalculatorFindsNextValidLeapDay(t *testing.T) {
	calculator := NewCalculator()
	next, err := calculator.Next("0 0 29 FEB *", "UTC", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestCalculatorUsesStandardCronDayOfMonthDayOfWeekOR(t *testing.T) {
	calculator := NewCalculator()
	after := time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC) // Tuesday, before Monday the 12th and day 13.
	next, err := calculator.Next("0 9 13 * MON", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want DOM/DOW OR match %s", next, want)
	}
}

func TestCalculatorTreatsUnrestrictedDayFieldAsCronANDSelector(t *testing.T) {
	calculator := NewCalculator()
	after := time.Date(2026, 1, 6, 8, 0, 0, 0, time.UTC) // Tuesday.
	next, err := calculator.Next("0 9 */1 * MON", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want wildcard-DOM/DOW selector %s", next, want)
	}
}

func TestCalculatorUsesNumberStepThroughFieldMaximum(t *testing.T) {
	calculator := NewCalculator()
	after := time.Date(2026, 1, 5, 9, 6, 0, 0, time.UTC)
	next, err := calculator.Next("5/15 9 * * *", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 5, 9, 20, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want number/step continuation %s", next, want)
	}
}

func TestCalculatorSupportsFiveFieldNamesRangesListsAndSteps(t *testing.T) {
	calculator := NewCalculator()
	after := time.Date(2026, 1, 5, 9, 1, 0, 0, time.UTC) // Monday in January.
	next, err := calculator.Next("*/15 9-10 * JAN MON-FRI", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestCalculatorAcceptsBothSundayNumbers(t *testing.T) {
	calculator := NewCalculator()
	after := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) // Monday.
	zero, err := calculator.Next("0 0 * * 0", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	seven, err := calculator.Next("0 0 * * 7", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	if !zero.Equal(seven) || zero.Weekday() != time.Sunday {
		t.Fatalf("Sunday 0=%s Sunday 7=%s", zero, seven)
	}
}

func TestCalculatorRejectsUnsupportedOrOutOfRangeCronGrammar(t *testing.T) {
	calculator := NewCalculator()
	for _, rule := range []string{
		"0 0 0 * * *", // no seconds field in the scheduler contract
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * * 13 *",
		"* * * * 8",
		"*/0 * * * *",
		"10-5 * * * *",
		"@reboot",
	} {
		t.Run(rule, func(t *testing.T) {
			if err := calculator.Validate(rule, "UTC"); err == nil {
				t.Fatalf("unsupported cron rule %q must be rejected", rule)
			}
		})
	}
}
