package recurrence

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type cronExpression struct {
	minute     cronField
	hour       cronField
	dayOfMonth cronField
	month      cronField
	dayOfWeek  cronField
}

type cronField struct {
	allowed  map[int]struct{}
	wildcard bool
}

var descriptors = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var weekdayNames = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

func parseCron(rule string) (cronExpression, error) {
	rule = strings.TrimSpace(rule)
	if expanded, ok := descriptors[strings.ToLower(rule)]; ok {
		rule = expanded
	}
	parts := strings.Fields(rule)
	if len(parts) != 5 {
		return cronExpression{}, errors.New("cron rule must contain exactly five fields")
	}
	minute, err := parseCronField(parts[0], 0, 59, nil, false)
	if err != nil {
		return cronExpression{}, fmt.Errorf("minute field: %w", err)
	}
	hour, err := parseCronField(parts[1], 0, 23, nil, false)
	if err != nil {
		return cronExpression{}, fmt.Errorf("hour field: %w", err)
	}
	dayOfMonth, err := parseCronField(parts[2], 1, 31, nil, false)
	if err != nil {
		return cronExpression{}, fmt.Errorf("day-of-month field: %w", err)
	}
	month, err := parseCronField(parts[3], 1, 12, monthNames, false)
	if err != nil {
		return cronExpression{}, fmt.Errorf("month field: %w", err)
	}
	dayOfWeek, err := parseCronField(parts[4], 0, 7, weekdayNames, true)
	if err != nil {
		return cronExpression{}, fmt.Errorf("day-of-week field: %w", err)
	}
	expression := cronExpression{minute: minute, hour: hour, dayOfMonth: dayOfMonth, month: month, dayOfWeek: dayOfWeek}
	if !expression.hasPossibleCalendarDate() {
		return cronExpression{}, errors.New("cron day-of-month has no valid date in the selected months")
	}
	return expression, nil
}

func parseCronField(raw string, minimum, maximum int, names map[string]int, normalizeSunday bool) (cronField, error) {
	field := cronField{allowed: make(map[int]struct{})}
	for _, item := range strings.Split(strings.ToUpper(raw), ",") {
		if item == "" {
			return cronField{}, errors.New("empty list element")
		}
		base, step, err := splitStep(item)
		if err != nil {
			return cronField{}, err
		}
		if base == "*" && step == 1 {
			field.wildcard = true
		}
		start, end, err := parseRange(base, minimum, maximum, names)
		if err != nil {
			return cronField{}, err
		}
		if strings.Contains(item, "/") && base != "*" && !strings.Contains(base, "-") {
			end = maximum
		}
		for value := start; value <= end; value += step {
			if normalizeSunday && value == 7 {
				field.allowed[0] = struct{}{}
				continue
			}
			field.allowed[value] = struct{}{}
		}
	}
	return field, nil
}

func splitStep(item string) (string, int, error) {
	parts := strings.Split(item, "/")
	if len(parts) > 2 {
		return "", 0, errors.New("field contains more than one step separator")
	}
	if len(parts) == 1 {
		return item, 1, nil
	}
	step, err := strconv.Atoi(parts[1])
	if err != nil || step < 1 {
		return "", 0, errors.New("step must be a positive integer")
	}
	return parts[0], step, nil
}

func parseRange(raw string, minimum, maximum int, names map[string]int) (int, int, error) {
	if raw == "*" {
		return minimum, maximum, nil
	}
	parts := strings.Split(raw, "-")
	if len(parts) > 2 {
		return 0, 0, errors.New("field contains an invalid range")
	}
	start, err := parseCronValue(parts[0], names)
	if err != nil {
		return 0, 0, err
	}
	end := start
	if len(parts) == 2 {
		end, err = parseCronValue(parts[1], names)
		if err != nil {
			return 0, 0, err
		}
	}
	if start < minimum || start > maximum || end < minimum || end > maximum || start > end {
		return 0, 0, fmt.Errorf("range %d-%d is outside %d-%d", start, end, minimum, maximum)
	}
	return start, end, nil
}

func parseCronValue(raw string, names map[string]int) (int, error) {
	if names != nil {
		if value, ok := names[raw]; ok {
			return value, nil
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("value %q is not valid", raw)
	}
	return value, nil
}

func (e cronExpression) matches(local time.Time) bool {
	if !e.minute.contains(local.Minute()) || !e.hour.contains(local.Hour()) || !e.month.contains(int(local.Month())) {
		return false
	}
	dayOfMonthMatches := e.dayOfMonth.contains(local.Day())
	dayOfWeekMatches := e.dayOfWeek.contains(int(local.Weekday()))
	switch {
	case e.dayOfMonth.wildcard && e.dayOfWeek.wildcard:
		return true
	case e.dayOfMonth.wildcard:
		return dayOfWeekMatches
	case e.dayOfWeek.wildcard:
		return dayOfMonthMatches
	default:
		return dayOfMonthMatches || dayOfWeekMatches
	}
}

func (e cronExpression) hasPossibleCalendarDate() bool {
	// When day-of-week is restricted, standard five-field cron uses DOM/DOW
	// OR semantics, so an allowed weekday always supplies a valid date. A
	// restricted DOM with wildcard DOW must itself exist in a selected month.
	if !e.dayOfWeek.wildcard || e.dayOfMonth.wildcard {
		return true
	}
	for month := 1; month <= 12; month++ {
		if !e.month.contains(month) {
			continue
		}
		maximumDay := time.Date(2000, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
		for day := 1; day <= maximumDay; day++ {
			if e.dayOfMonth.contains(day) {
				return true
			}
		}
	}
	return false
}

func (f cronField) contains(value int) bool {
	_, ok := f.allowed[value]
	return ok
}
