package transporthttp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

type scheduleResponse struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Schedule      string          `json:"schedule,omitempty"`
	RunAt         *string         `json:"run_at,omitempty"`
	Timezone      string          `json:"timezone"`
	URL           string          `json:"url"`
	Method        string          `json:"method"`
	Body          json.RawMessage `json:"body"`
	Retry         retryResponse   `json:"retry"`
	MisfirePolicy string          `json:"misfire_policy"`
	CatchUpLimit  int             `json:"catch_up_limit"`
	OverlapPolicy string          `json:"overlap_policy"`
	Paused        bool            `json:"paused"`
	NextDueAt     *string         `json:"next_due_at"`
	Version       int64           `json:"version"`
}

type currentScheduleResponse struct {
	scheduleResponse
	Status string `json:"status"`
}

type retryResponse struct {
	MaxAttempts           int `json:"max_attempts"`
	BackoffSeconds        int `json:"backoff_seconds"`
	MaxBackoffSeconds     int `json:"max_backoff_seconds"`
	MaxRetryWindowSeconds int `json:"max_retry_window_seconds"`
}

func toScheduleResponse(schedule domain.Schedule) scheduleResponse {
	var nextDueAt, runAt *string
	if !schedule.NextDueAt.IsZero() {
		value := domain.NormalizeInstant(schedule.NextDueAt).Format(time.RFC3339Nano)
		nextDueAt = &value
	}
	if !schedule.RunAt.IsZero() {
		value := domain.NormalizeInstant(schedule.RunAt).Format(time.RFC3339Nano)
		runAt = &value
	}
	return scheduleResponse{
		ID: schedule.ID, Name: schedule.Name, Schedule: schedule.Rule, RunAt: runAt, Timezone: schedule.Timezone,
		URL: schedule.TargetURL, Method: string(schedule.Method), Body: append(json.RawMessage(nil), schedule.Payload...),
		Retry: retryResponse{
			MaxAttempts: schedule.Retry.MaxAttempts, BackoffSeconds: schedule.Retry.BackoffSeconds,
			MaxBackoffSeconds: schedule.Retry.MaxBackoffSeconds, MaxRetryWindowSeconds: schedule.Retry.MaxRetryWindowSeconds,
		},
		MisfirePolicy: string(schedule.MisfirePolicy), CatchUpLimit: schedule.CatchUpLimit,
		OverlapPolicy: string(schedule.OverlapPolicy), Paused: schedule.Status == domain.SchedulePaused,
		NextDueAt: nextDueAt, Version: schedule.Version,
	}
}

func fingerprint(scope string, payload []byte) string {
	digest := sha256.Sum256(append([]byte(scope+"\n"), payload...))
	return fmt.Sprintf("sha256:%x", digest[:])
}

func receiptResponse(receipt domain.CommandReceipt) (int, map[string]any) {
	switch receipt.Result.Code {
	case domain.ResultExhausted:
		return http.StatusConflict, errorResponse("schedule_exhausted", "scheduler schedule has no further triggers; replace it explicitly to schedule again", receipt.RequestID)
	case domain.ResultChanged:
		return http.StatusConflict, errorResponse("schedule_changed", "scheduler schedule changed during the command", receipt.RequestID)
	case domain.ResultAlreadyExists:
		return http.StatusConflict, errorResponse("schedule_already_exists", "scheduler schedule already exists", receipt.RequestID)
	case domain.ResultNotFound:
		return http.StatusNotFound, errorResponse("schedule_not_found", "scheduler schedule was not found", receipt.RequestID)
	case domain.ResultRegistered, domain.ResultUpdated, domain.ResultPaused, domain.ResultResumed, domain.ResultDeleted:
		data := map[string]any{"name": receipt.Result.Name, "status": receipt.Result.Code}
		if receipt.Result.Schedule != nil {
			data["schedule"] = toScheduleResponse(*receipt.Result.Schedule)
		}
		return http.StatusOK, map[string]any{"data": data, "meta": map[string]string{"request_id": receipt.RequestID}}
	default:
		return http.StatusInternalServerError, errorResponse("scheduler_response_invalid", "scheduler command result is invalid", receipt.RequestID)
	}
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
