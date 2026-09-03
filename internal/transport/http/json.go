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

type jobRequest struct {
	Name          string          `json:"name"`
	Schedule      string          `json:"schedule"`
	URL           string          `json:"url"`
	Method        string          `json:"method"`
	Body          json.RawMessage `json:"body"`
	Retry         *retryRequest   `json:"retry"`
	MisfirePolicy string          `json:"misfire_policy"`
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

func (r jobRequest) toDomain(pathName string) (domain.Job, error) {
	if r.Name == "" {
		r.Name = pathName
	} else if r.Name != pathName {
		return domain.Job{}, errors.New("job name must match the path")
	}
	retry, err := r.Retry.toDomain()
	if err != nil {
		return domain.Job{}, err
	}
	return domain.Job{
		Name: r.Name, Schedule: r.Schedule, URL: r.URL, Method: domain.Method(r.Method), Body: r.Body,
		Retry:         retry,
		MisfirePolicy: domain.MisfirePolicy(r.MisfirePolicy), Paused: r.Paused,
	}.Normalized()
}

var (
	errUnsupportedMediaType = errors.New("Content-Type must be application/json")
	errInvalidAction        = errors.New("action body must be an empty JSON object")
	errInvalidDelete        = errors.New("delete body must be an empty JSON object")
	errInvalidJob           = errors.New("invalid scheduler job")
)

func decodePayload(r *http.Request, pathName, action string) (domain.Job, []byte, error) {
	body, err := readBody(r)
	if err != nil {
		return domain.Job{}, nil, err
	}
	if action != "" {
		return emptyObjectPayload(body, errInvalidAction, r.Header.Get("Content-Type"))
	}
	if r.Method == http.MethodDelete {
		return emptyObjectPayload(body, errInvalidDelete, r.Header.Get("Content-Type"))
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return domain.Job{}, nil, errUnsupportedMediaType
	}
	if len(body) == 0 {
		return domain.Job{}, nil, errInvalidJob
	}
	if err := ensureNoDuplicateKeys(body); err != nil {
		return domain.Job{}, nil, err
	}
	var raw map[string]json.RawMessage
	if err := decodeSingleJSON(body, &raw); err != nil || raw == nil {
		return domain.Job{}, nil, errInvalidJob
	}
	for key, value := range raw {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return domain.Job{}, nil, fmt.Errorf("job field %q must not be null", key)
		}
	}
	if retryBody, exists := raw["retry"]; exists {
		var retryFields map[string]json.RawMessage
		if err := decodeSingleJSON(retryBody, &retryFields); err != nil || retryFields == nil {
			return domain.Job{}, nil, errors.New("job field \"retry\" must be a JSON object")
		}
		for key, value := range retryFields {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return domain.Job{}, nil, fmt.Errorf("retry field %q must not be null", key)
			}
		}
	}
	var request jobRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return domain.Job{}, nil, fmt.Errorf("decode job: %w", err)
	}
	normalized, err := request.toDomain(pathName)
	if err != nil {
		return domain.Job{}, nil, err
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return domain.Job{}, nil, err
	}
	return normalized, canonical, nil
}

func emptyObjectPayload(body []byte, invalid error, contentType string) (domain.Job, []byte, error) {
	if len(body) == 0 {
		return domain.Job{}, []byte(`{}`), nil
	}
	if !isJSONContentType(contentType) {
		return domain.Job{}, nil, errUnsupportedMediaType
	}
	if err := ensureNoDuplicateKeys(body); err != nil {
		return domain.Job{}, nil, err
	}
	var object map[string]json.RawMessage
	if err := decodeSingleJSON(body, &object); err != nil || object == nil || len(object) != 0 {
		return domain.Job{}, nil, invalid
	}
	return domain.Job{}, []byte(`{}`), nil
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
	case errors.Is(err, errInvalidAction), errors.Is(err, errInvalidDelete), errors.Is(err, errInvalidJob):
		return http.StatusBadRequest, "invalid_job", err.Error()
	case strings.Contains(err.Error(), "request body too large"):
		return http.StatusRequestEntityTooLarge, "request_body_too_large", "request body is too large"
	default:
		return http.StatusBadRequest, "invalid_job", err.Error()
	}
}
