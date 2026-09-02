package scheduler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const internalTestToken = "service-token"

func newInternalTestHandler() (*Service, http.Handler) {
	service := NewService(&testRunner{}, nil)
	return service, NewHTTPHandler(service, internalTestToken)
}

func internalRequest(method, path, body, requestID, idempotencyKey string) *http.Request {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+internalTestToken)
	if requestID != "" {
		request.Header.Set("X-Request-Id", requestID)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func decodeInternalResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
	return payload
}

func TestInternalHTTPRegistersUpdatesAndDeletesGenericJobs(t *testing.T) {
	service, handler := newInternalTestHandler()
	path := "/internal/scheduler/v1/jobs/daily.report"
	body := `{"schedule":"@every 1m","url":"http://service.test/command","retry":{"max_attempts":2}}`

	create := httptest.NewRecorder()
	handler.ServeHTTP(create, internalRequest(http.MethodPost, path, body, "req-create", "create-1"))
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200: %s", create.Code, create.Body.String())
	}
	created, ok := service.Job("daily.report")
	if !ok || created.Name != "daily.report" || created.Retry.MaxAttempts != 2 {
		t.Fatalf("created job = %#v, exists=%v", created, ok)
	}
	if got := create.Header().Get("X-Request-Id"); got != "req-create" {
		t.Fatalf("create response request id = %q, want req-create", got)
	}

	update := httptest.NewRecorder()
	updatedBody := `{"name":"daily.report","schedule":"0 9 * * *","url":"http://service.test/updated"}`
	handler.ServeHTTP(update, internalRequest(http.MethodPut, path, updatedBody, "req-update", "update-1"))
	if update.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200: %s", update.Code, update.Body.String())
	}
	updated, ok := service.Job("daily.report")
	if !ok || updated.Schedule != "0 9 * * *" || updated.URL != "http://service.test/updated" {
		t.Fatalf("updated job = %#v, exists=%v", updated, ok)
	}

	remove := httptest.NewRecorder()
	handler.ServeHTTP(remove, internalRequest(http.MethodDelete, path, "", "req-delete", "delete-1"))
	if remove.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200: %s", remove.Code, remove.Body.String())
	}
	if _, ok := service.Job("daily.report"); ok {
		t.Fatal("deleted job is still registered")
	}
}

func TestInternalHTTPRequiresServiceTokenRequestIDAndIdempotencyKey(t *testing.T) {
	_, handler := newInternalTestHandler()
	path := "/internal/scheduler/v1/jobs/job"
	body := `{"schedule":"@every 1m","url":"http://service.test/command"}`

	tests := []struct {
		name      string
		request   *http.Request
		status    int
		errorCode string
	}{
		{
			name: "missing token",
			request: func() *http.Request {
				r := internalRequest(http.MethodPost, path, body, "req-1", "key-1")
				r.Header.Del("Authorization")
				return r
			}(),
			status:    http.StatusUnauthorized,
			errorCode: "service_auth_failed",
		},
		{
			name:      "missing request id",
			request:   internalRequest(http.MethodPost, path, body, "", "key-1"),
			status:    http.StatusBadRequest,
			errorCode: "request_id_required",
		},
		{
			name:      "missing idempotency key",
			request:   internalRequest(http.MethodPost, path, body, "req-1", ""),
			status:    http.StatusBadRequest,
			errorCode: "idempotency_key_required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, test.request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			payload := decodeInternalResponse(t, response)
			errorPayload, ok := payload["error"].(map[string]any)
			if !ok || errorPayload["code"] != test.errorCode {
				t.Fatalf("error payload = %#v, want code %q", payload["error"], test.errorCode)
			}
		})
	}
}

func TestInternalHTTPRejectsNonStrictJobJSON(t *testing.T) {
	_, handler := newInternalTestHandler()
	path := "/internal/scheduler/v1/jobs/job"
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"schedule":"@every 1m","url":"http://service.test/command","unexpected":true}`},
		{name: "trailing value", body: `{"schedule":"@every 1m","url":"http://service.test/command"}{}`},
		{name: "null schedule", body: `{"schedule":null,"url":"http://service.test/command"}`},
		{name: "retry unknown field", body: `{"schedule":"@every 1m","url":"http://service.test/command","retry":{"attempts":2}}`},
		{name: "duplicate field", body: `{"schedule":"@every 1m","schedule":"@every 2m","url":"http://service.test/command"}`},
		{name: "name does not match path", body: `{"name":"other","schedule":"@every 1m","url":"http://service.test/command"}`},
		{name: "non-http URL", body: `{"schedule":"@every 1m","url":"file:///tmp/command"}`},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := internalRequest(http.MethodPost, path, test.body, "req-invalid", "invalid-"+string(rune('a'+index)))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
			}
			payload := decodeInternalResponse(t, response)
			if payload["error"].(map[string]any)["code"] != "invalid_job" {
				t.Fatalf("error payload = %#v", payload["error"])
			}
		})
	}

	wrongContentType := internalRequest(http.MethodPost, path, `{"schedule":"@every 1m","url":"http://service.test/command"}`, "req-content-type", "content-type-1")
	wrongContentType.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, wrongContentType)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type status = %d, want 415", response.Code)
	}
}

func TestInternalHTTPAcceptsKokoroTransportAliases(t *testing.T) {
	service, handler := newInternalTestHandler()
	request := httptest.NewRequest(http.MethodPost, "/internal/scheduler/v1/jobs/alias-job", bytes.NewBufferString(`{"schedule":"@every 1m","url":"http://service.test/command"}`))
	request.Header.Set("X-Kokoro-Internal-Secret", internalTestToken)
	request.Header.Set("X-Kokoro-Request-Id", "req-alias")
	request.Header.Set("Idempotency-Key", "alias-1")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if _, ok := service.Job("alias-job"); !ok {
		t.Fatal("alias request did not register the job")
	}
}

func TestInternalHTTPIdempotencyReplaysAndDetectsConflicts(t *testing.T) {
	service, handler := newInternalTestHandler()
	path := "/internal/scheduler/v1/jobs/job"
	body := `{"schedule":"@every 1m","url":"http://service.test/command"}`

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, internalRequest(http.MethodPost, path, body, "req-first", "same-key"))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d: %s", first.Code, first.Body.String())
	}

	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, internalRequest(http.MethodPost, path, body, "req-replay", "same-key"))
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = status %d body %q, want status %d body %q", replay.Code, replay.Body.String(), first.Code, first.Body.String())
	}
	if got := replay.Header().Get("X-Request-Id"); got != "req-first" {
		t.Fatalf("replay request id = %q, want original req-first", got)
	}

	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, internalRequest(http.MethodPost, path, `{"schedule":"@every 2m","url":"http://service.test/command"}`, "req-conflict", "same-key"))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409: %s", conflict.Code, conflict.Body.String())
	}
	if payload := decodeInternalResponse(t, conflict); payload["error"].(map[string]any)["code"] != "idempotency_conflict" {
		t.Fatalf("conflict error = %#v", payload["error"])
	}

	if job, ok := service.Job("job"); !ok || job.Schedule != "@every 1m" {
		t.Fatalf("conflict changed registered job: %#v, exists=%v", job, ok)
	}

	duplicate := httptest.NewRecorder()
	handler.ServeHTTP(duplicate, internalRequest(http.MethodPost, path, body, "req-duplicate", "different-key"))
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409: %s", duplicate.Code, duplicate.Body.String())
	}
	if payload := decodeInternalResponse(t, duplicate); payload["error"].(map[string]any)["code"] != "job_already_exists" {
		t.Fatalf("duplicate error = %#v", payload["error"])
	}
}

func TestInternalHTTPPauseAndResumeUseExistingServiceControls(t *testing.T) {
	runner := &testRunner{}
	service := NewService(runner, nil)
	handler := NewHTTPHandler(service, internalTestToken)
	path := "/internal/scheduler/v1/jobs/job"

	create := httptest.NewRecorder()
	handler.ServeHTTP(create, internalRequest(http.MethodPost, path, `{"schedule":"@every 1m","url":"http://service.test/command","paused":true}`, "req-create", "pause-create"))
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	job, _ := service.Job("job")
	service.run(job, unixTime(100))
	if runner.calls != 0 {
		t.Fatalf("paused runner calls = %d, want 0", runner.calls)
	}

	resume := httptest.NewRecorder()
	handler.ServeHTTP(resume, internalRequest(http.MethodPost, path+"/resume", `{}`, "req-resume", "pause-resume"))
	if resume.Code != http.StatusOK {
		t.Fatalf("resume status = %d: %s", resume.Code, resume.Body.String())
	}
	service.run(job, unixTime(100))
	if runner.calls != 1 {
		t.Fatalf("resumed runner calls = %d, want 1", runner.calls)
	}

	pause := httptest.NewRecorder()
	handler.ServeHTTP(pause, internalRequest(http.MethodPost, path+"/pause", `{}`, "req-pause", "pause-again"))
	if pause.Code != http.StatusOK {
		t.Fatalf("pause status = %d: %s", pause.Code, pause.Body.String())
	}
	service.run(job, unixTime(101))
	if runner.calls != 1 {
		t.Fatalf("paused-after-resume runner calls = %d, want 1", runner.calls)
	}
}

func TestHTTPHandlerKeepsUnauthenticatedHealthAndReadinessProbes(t *testing.T) {
	_, handler := newInternalTestHandler()
	for _, path := range []string{"/healthz", "/readyz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, response.Code)
		}
		if response.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s content type = %q", path, response.Header().Get("Content-Type"))
		}
	}

	wrongMethod := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status = %d, want 405", wrongMethod.Code)
	}
}

func unixTime(seconds int64) (atTime time.Time) {
	return time.Unix(seconds, 0).UTC()
}
