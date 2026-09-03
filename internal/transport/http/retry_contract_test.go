package transporthttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodePayloadAcceptsBoundedRetryPolicy(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/internal/scheduler/v1/jobs/job", strings.NewReader(`{
		"schedule":"@every 1m",
		"url":"http://service.test/command",
		"retry":{
			"max_attempts":4,
			"backoff_seconds":2,
			"max_backoff_seconds":10,
			"max_retry_window_seconds":60
		}
	}`))
	request.Header.Set("Content-Type", "application/json")
	job, _, err := decodePayload(request, "job", "")
	if err != nil {
		t.Fatal(err)
	}
	if job.Retry.MaxAttempts != 4 || job.Retry.BackoffSeconds != 2 || job.Retry.MaxBackoffSeconds != 10 || job.Retry.MaxRetryWindowSeconds != 60 {
		t.Fatalf("retry policy = %#v", job.Retry)
	}
}

func TestDecodePayloadRejectsZeroRetryBoundary(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/internal/scheduler/v1/jobs/job", strings.NewReader(`{
		"schedule":"@every 1m",
		"url":"http://service.test/command",
		"retry":{"max_attempts":2,"backoff_seconds":0}
	}`))
	request.Header.Set("Content-Type", "application/json")
	_, _, err := decodePayload(request, "job", "")
	if err == nil || !strings.Contains(err.Error(), "backoff_seconds") {
		t.Fatalf("error = %v, want backoff_seconds boundary error", err)
	}
}

func TestDecodePayloadRejectsNullRetryBoundary(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/internal/scheduler/v1/jobs/job", strings.NewReader(`{
		"schedule":"@every 1m",
		"url":"http://service.test/command",
		"retry":{"max_attempts":2,"max_backoff_seconds":null}
	}`))
	request.Header.Set("Content-Type", "application/json")
	_, _, err := decodePayload(request, "job", "")
	if err == nil || !strings.Contains(err.Error(), "max_backoff_seconds") {
		t.Fatalf("error = %v, want max_backoff_seconds null error", err)
	}
}

func TestRegistrationResponseIncludesRetryBounds(t *testing.T) {
	handler := NewHTTPHandler(newStubScheduler(), testToken, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, internalRequest(
		http.MethodPost,
		JobsPathPrefix+"job",
		`{"schedule":"@every 1m","url":"http://service.test/command","retry":{"max_attempts":4,"backoff_seconds":2,"max_backoff_seconds":10,"max_retry_window_seconds":60}}`,
		"req-retry-bounds",
		"retry-bounds-key",
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Job struct {
				Retry struct {
					MaxAttempts           int `json:"max_attempts"`
					BackoffSeconds        int `json:"backoff_seconds"`
					MaxBackoffSeconds     int `json:"max_backoff_seconds"`
					MaxRetryWindowSeconds int `json:"max_retry_window_seconds"`
				} `json:"retry"`
			} `json:"job"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	retry := envelope.Data.Job.Retry
	if retry.MaxAttempts != 4 || retry.BackoffSeconds != 2 || retry.MaxBackoffSeconds != 10 || retry.MaxRetryWindowSeconds != 60 {
		t.Fatalf("response retry policy = %#v", retry)
	}
}
