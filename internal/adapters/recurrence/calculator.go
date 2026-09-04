package recurrence

import (
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // Keep IANA rules available in the distroless production image.
)

const (
	minimumInterval = time.Second
	maximumInterval = 366 * 24 * time.Hour
	searchYears     = 10
)

type Calculator struct{}

func NewCalculator() Calculator { return Calculator{} }

func (Calculator) Validate(rule, timezone string) error {
	if strings.HasPrefix(rule, "TZ=") || strings.HasPrefix(rule, "CRON_TZ=") {
		return errors.New("timezone prefix is not allowed in schedule rule")
	}
	if _, err := loadLocation(timezone); err != nil {
		return err
	}
	if strings.HasPrefix(rule, "@every ") {
		_, err := parseInterval(rule)
		return err
	}
	_, err := parseCron(rule)
	return err
}

func (c Calculator) Next(rule, timezone string, after time.Time) (time.Time, error) {
	if err := c.Validate(rule, timezone); err != nil {
		return time.Time{}, err
	}
	after = after.UTC()
	if strings.HasPrefix(rule, "@every ") {
		interval, _ := parseInterval(rule)
		return after.Add(interval).UTC().Truncate(time.Millisecond), nil
	}
	location, _ := loadLocation(timezone)
	expression, _ := parseCron(rule)
	candidate := after.Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(searchYears, 0, 0)
	for !candidate.After(limit) {
		if expression.matches(candidate.In(location)) {
			return candidate.UTC().Truncate(time.Millisecond), nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, errors.New("schedule has no occurrence within the supported search horizon")
}

func (c Calculator) NextAfter(rule, timezone string, anchor, threshold time.Time) (time.Time, error) {
	if err := c.Validate(rule, timezone); err != nil {
		return time.Time{}, err
	}
	anchor = anchor.UTC().Truncate(time.Millisecond)
	threshold = threshold.UTC().Truncate(time.Millisecond)
	if strings.HasPrefix(rule, "@every ") {
		interval, _ := parseInterval(rule)
		if anchor.After(threshold) {
			return anchor, nil
		}
		steps := threshold.Sub(anchor)/interval + 1
		return anchor.Add(steps * interval).UTC().Truncate(time.Millisecond), nil
	}
	return c.Next(rule, timezone, threshold)
}

func loadLocation(name string) (*time.Location, error) {
	if name == "" {
		name = "UTC"
	}
	if name != "UTC" && !strings.Contains(name, "/") {
		return nil, errors.New("timezone must be UTC or an IANA area/location name")
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", name, err)
	}
	return location, nil
}

func parseInterval(rule string) (time.Duration, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(rule, "@every "))
	interval, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse @every interval: %w", err)
	}
	if interval < minimumInterval || interval > maximumInterval {
		return 0, fmt.Errorf("@every interval must be between %s and %s", minimumInterval, maximumInterval)
	}
	if interval%time.Millisecond != 0 {
		return 0, errors.New("@every interval must use millisecond precision")
	}
	return interval, nil
}
