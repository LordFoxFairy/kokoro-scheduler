package transporthttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

type scheduleRequest struct {
	Name          string          `json:"name"`
	Schedule      string          `json:"schedule"`
	Timezone      string          `json:"timezone"`
	URL           string          `json:"url"`
	Method        string          `json:"method"`
	Body          json.RawMessage `json:"body"`
	Retry         *retryRequest   `json:"retry"`
	MisfirePolicy string          `json:"misfire_policy"`
	CatchUpLimit  int             `json:"catch_up_limit"`
	OverlapPolicy string          `json:"overlap_policy"`
	Paused        bool            `json:"paused"`
}

type retryRequest struct {
	MaxAttempts           *int `json:"max_attempts"`
	BackoffSeconds        *int `json:"backoff_seconds"`
	MaxBackoffSeconds     *int `json:"max_backoff_seconds"`
	MaxRetryWindowSeconds *int `json:"max_retry_window_seconds"`
}

func (r *retryRequest) toDomain() (domain.RetryPolicy, error) {
	policy := domain.DefaultRetryPolicy()
	if r == nil {
		return policy, nil
	}
	if r.MaxAttempts != nil {
		policy.MaxAttempts = *r.MaxAttempts
	}
	if r.BackoffSeconds != nil {
		policy.BackoffSeconds = *r.BackoffSeconds
	}
	if r.MaxBackoffSeconds != nil {
		policy.MaxBackoffSeconds = *r.MaxBackoffSeconds
	}
	if r.MaxRetryWindowSeconds != nil {
		policy.MaxRetryWindowSeconds = *r.MaxRetryWindowSeconds
	}
	if err := policy.Validate(); err != nil {
		return domain.RetryPolicy{}, err
	}
	return policy, nil
}

func (r scheduleRequest) toDomain(tenantID, pathName string) (domain.Schedule, error) {
	if r.Name == "" {
		r.Name = pathName
	} else if r.Name != pathName {
		return domain.Schedule{}, errors.New("schedule name must match the path")
	}
	retry, err := r.Retry.toDomain()
	if err != nil {
		return domain.Schedule{}, err
	}
	status := domain.ScheduleActive
	if r.Paused {
		status = domain.SchedulePaused
	}
	return (domain.Schedule{
		TenantID: tenantID, Name: r.Name, Rule: r.Schedule, Timezone: r.Timezone,
		TargetURL: r.URL, Method: domain.Method(r.Method), Payload: r.Body,
		Retry: retry, MisfirePolicy: domain.MisfirePolicy(r.MisfirePolicy),
		CatchUpLimit: r.CatchUpLimit, OverlapPolicy: domain.OverlapPolicy(r.OverlapPolicy),
		Status: status,
	}).Normalized()
}

var (
	errUnsupportedMediaType = errors.New("Content-Type must be application/json")
	errInvalidAction        = errors.New("action body must be an empty JSON object")
	errInvalidDelete        = errors.New("delete body must be an empty JSON object")
	errInvalidSchedule      = errors.New("invalid scheduler schedule")
)

func decodePayload(r *http.Request, tenantID, pathName, action string) (domain.Schedule, []byte, error) {
	body, err := readBody(r)
	if err != nil {
		return domain.Schedule{}, nil, err
	}
	if action != "" {
		return emptyObjectPayload(body, errInvalidAction, r.Header.Get("Content-Type"))
	}
	if r.Method == http.MethodDelete {
		return emptyObjectPayload(body, errInvalidDelete, r.Header.Get("Content-Type"))
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return domain.Schedule{}, nil, errUnsupportedMediaType
	}
	if len(body) == 0 {
		return domain.Schedule{}, nil, errInvalidSchedule
	}
	if err := ensureNoDuplicateKeys(body); err != nil {
		return domain.Schedule{}, nil, err
	}
	var raw map[string]json.RawMessage
	if err := decodeSingleJSON(body, &raw); err != nil || raw == nil {
		return domain.Schedule{}, nil, errInvalidSchedule
	}
	for key, value := range raw {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return domain.Schedule{}, nil, fmt.Errorf("schedule field %q must not be null", key)
		}
	}
	if retryBody, exists := raw["retry"]; exists {
		var retryFields map[string]json.RawMessage
		if err := decodeSingleJSON(retryBody, &retryFields); err != nil || retryFields == nil {
			return domain.Schedule{}, nil, errors.New("schedule field \"retry\" must be a JSON object")
		}
		for key, value := range retryFields {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return domain.Schedule{}, nil, fmt.Errorf("retry field %q must not be null", key)
			}
		}
	}
	var request scheduleRequest
	if err := decodeSingleJSON(body, &request); err != nil {
		return domain.Schedule{}, nil, fmt.Errorf("decode schedule: %w", err)
	}
	for field, value := range map[string]string{
		"name": request.Name, "timezone": request.Timezone, "method": request.Method,
		"misfire_policy": request.MisfirePolicy, "overlap_policy": request.OverlapPolicy,
	} {
		if _, provided := raw[field]; provided && strings.TrimSpace(value) == "" {
			return domain.Schedule{}, nil, fmt.Errorf("schedule field %q must not be empty", field)
		}
	}
	if _, provided := raw["catch_up_limit"]; provided && request.CatchUpLimit == 0 {
		return domain.Schedule{}, nil, errors.New("schedule field \"catch_up_limit\" must be at least 1")
	}
	normalized, err := request.toDomain(tenantID, pathName)
	if err != nil {
		return domain.Schedule{}, nil, err
	}
	canonical, err := canonicalCommandPayload(normalized)
	if err != nil {
		return domain.Schedule{}, nil, err
	}
	return normalized, canonical, nil
}

func canonicalCommandPayload(schedule domain.Schedule) ([]byte, error) {
	return json.Marshal(struct {
		TenantID      string                `json:"tenant_id"`
		Name          string                `json:"name"`
		Schedule      string                `json:"schedule"`
		Timezone      string                `json:"timezone"`
		URL           string                `json:"url"`
		Method        domain.Method         `json:"method"`
		Body          json.RawMessage       `json:"body"`
		Retry         domain.RetryPolicy    `json:"retry"`
		MisfirePolicy domain.MisfirePolicy  `json:"misfire_policy"`
		CatchUpLimit  int                   `json:"catch_up_limit"`
		OverlapPolicy domain.OverlapPolicy  `json:"overlap_policy"`
		Status        domain.ScheduleStatus `json:"status"`
	}{
		TenantID: schedule.TenantID, Name: schedule.Name, Schedule: schedule.Rule,
		Timezone: schedule.Timezone, URL: schedule.TargetURL, Method: schedule.Method,
		Body: schedule.Payload, Retry: schedule.Retry, MisfirePolicy: schedule.MisfirePolicy,
		CatchUpLimit: schedule.CatchUpLimit, OverlapPolicy: schedule.OverlapPolicy, Status: schedule.Status,
	})
}

func emptyObjectPayload(body []byte, invalid error, contentType string) (domain.Schedule, []byte, error) {
	if len(body) == 0 {
		return domain.Schedule{}, []byte(`{}`), nil
	}
	if !isJSONContentType(contentType) {
		return domain.Schedule{}, nil, errUnsupportedMediaType
	}
	if err := ensureNoDuplicateKeys(body); err != nil {
		return domain.Schedule{}, nil, err
	}
	var object map[string]json.RawMessage
	if err := decodeSingleJSON(body, &object); err != nil || object == nil || len(object) != 0 {
		return domain.Schedule{}, nil, invalid
	}
	return domain.Schedule{}, []byte(`{}`), nil
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxRequestBodyBytes {
		return nil, errors.New("request body too large")
	}
	return body, nil
}

func decodeSingleJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func ensureNoDuplicateKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := walkJSON(decoder); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func walkJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func decodeError(err error) (int, string, string) {
	switch {
	case errors.Is(err, errUnsupportedMediaType):
		return http.StatusUnsupportedMediaType, "unsupported_media_type", err.Error()
	case errors.Is(err, errInvalidAction), errors.Is(err, errInvalidDelete), errors.Is(err, errInvalidSchedule):
		return http.StatusBadRequest, "invalid_schedule", err.Error()
	case strings.Contains(err.Error(), "request body too large"):
		return http.StatusRequestEntityTooLarge, "request_body_too_large", "request body is too large"
	default:
		return http.StatusBadRequest, "invalid_schedule", err.Error()
	}
}
