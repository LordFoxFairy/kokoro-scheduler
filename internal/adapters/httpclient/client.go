package httpclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

const (
	OccurrenceHeader  = "X-Kokoro-Scheduler-Occurrence"
	JobHeader         = "X-Kokoro-Scheduler-Job"
	RequestIDHeader   = "X-Request-Id"
	IdempotencyHeader = "Idempotency-Key"
)

type Client struct {
	httpClient         *http.Client
	targetServiceToken string
}

func NewClient(httpClient *http.Client, targetServiceToken string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{httpClient: httpClient, targetServiceToken: strings.TrimSpace(targetServiceToken)}
}

func NewDefaultClient(timeout time.Duration, targetServiceToken string) *Client {
	return NewClient(&http.Client{Timeout: timeout}, targetServiceToken)
}

func (c *Client) Dispatch(ctx context.Context, job domain.Job, occurrence domain.Occurrence) domain.RunResult {
	requestID, idempotencyKey := domain.RequestIdentity(job, occurrence.ScheduledAt)
	traceID := domain.TraceIdentity(job, occurrence.ScheduledAt)
	body := job.Body
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	if !json.Valid(body) {
		return domain.RunResult{Err: errors.New("job body is invalid JSON"), Code: "SCHEDULER_TARGET_UNAVAILABLE", RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
	}
	request, err := http.NewRequestWithContext(ctx, string(job.Method), job.URL, bytes.NewReader(body))
	if err != nil {
		return domain.RunResult{Err: err, Code: "SCHEDULER_TARGET_UNAVAILABLE", RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(JobHeader, job.Name)
	request.Header.Set(OccurrenceHeader, occurrence.Identity())
	request.Header.Set(RequestIDHeader, requestID)
	request.Header.Set(IdempotencyHeader, idempotencyKey)
	request.Header.Set("traceparent", traceparent(traceID, requestID))
	if c.targetServiceToken != "" {
		request.Header.Set("Authorization", "Bearer "+c.targetServiceToken)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		code := "SCHEDULER_TARGET_UNAVAILABLE"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = "SCHEDULER_TARGET_TIMEOUT"
		}
		return domain.RunResult{Err: err, Code: code, RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
	}
	defer response.Body.Close()
	result := domain.RunResult{Status: response.StatusCode, RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
	if !result.Succeeded() {
		result.Code = "SCHEDULER_TARGET_REJECTED"
		result.Err = fmt.Errorf("job returned HTTP %d", response.StatusCode)
	}
	return result
}

func traceparent(traceID, requestID string) string {
	digest := sha256.Sum256([]byte(requestID + ":dispatch"))
	spanID := hex.EncodeToString(digest[:8])
	return "00-" + traceID + "-" + spanID + "-01"
}
