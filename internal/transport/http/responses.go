package transporthttp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

type jobResponse struct {
	Name          string          `json:"name"`
	Schedule      string          `json:"schedule"`
	URL           string          `json:"url"`
	Method        string          `json:"method"`
	Body          json.RawMessage `json:"body"`
	Retry         retryResponse   `json:"retry"`
	MisfirePolicy string          `json:"misfire_policy"`
	Paused        bool            `json:"paused"`
}

type retryResponse struct {
	MaxAttempts           int `json:"max_attempts"`
	BackoffSeconds        int `json:"backoff_seconds"`
	MaxBackoffSeconds     int `json:"max_backoff_seconds"`
	MaxRetryWindowSeconds int `json:"max_retry_window_seconds"`
}

func toJobResponse(job domain.Job) jobResponse {
	return jobResponse{
		Name: job.Name, Schedule: job.Schedule, URL: job.URL, Method: string(job.Method),
		Body: append(json.RawMessage(nil), job.Body...),
		Retry: retryResponse{
			MaxAttempts:           job.Retry.MaxAttempts,
			BackoffSeconds:        job.Retry.BackoffSeconds,
			MaxBackoffSeconds:     job.Retry.MaxBackoffSeconds,
			MaxRetryWindowSeconds: job.Retry.MaxRetryWindowSeconds,
		},
		MisfirePolicy: string(job.MisfirePolicy), Paused: job.Paused,
	}
}

func fingerprint(scope string, payload []byte) string {
	digest := sha256.Sum256(append([]byte(scope+"\n"), payload...))
	return fmt.Sprintf("sha256:%x", digest[:])
}

func responseRequestID(body []byte, defaultRequestID string) string {
	var envelope struct {
		Meta struct {
			RequestID string `json:"request_id"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Meta.RequestID != "" {
		return envelope.Meta.RequestID
	}
	return defaultRequestID
}

func errorResponse(code, message, requestID string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": message}, "meta": map[string]string{"request_id": requestID}}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":{"code":"scheduler_response_invalid","message":"scheduler response could not be encoded"}}`)
	}
	writeRawJSON(w, status, body)
}

func writeRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
