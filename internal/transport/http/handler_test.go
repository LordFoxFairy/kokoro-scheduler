package transporthttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

const testToken = "scheduler-internal-token"

type stubScheduler struct {
	mu   sync.Mutex
	jobs map[string]domain.Job
}

func newStubScheduler() *stubScheduler { return &stubScheduler{jobs: make(map[string]domain.Job)} }
func (s *stubScheduler) Register(job domain.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.Name]; exists {
		return domain.ErrJobAlreadyExists
	}
	s.jobs[job.Name] = job
	return nil
}
func (s *stubScheduler) Update(job domain.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.Name]; !exists {
		return domain.ErrJobNotFound
	}
	s.jobs[job.Name] = job
	return nil
}
func (s *stubScheduler) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[name]; !exists {
		return domain.ErrJobNotFound
	}
	delete(s.jobs, name)
	return nil
}
func (s *stubScheduler) Pause(name string) error  { return s.setPaused(name, true) }
func (s *stubScheduler) Resume(name string) error { return s.setPaused(name, false) }
func (s *stubScheduler) setPaused(name string, paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, exists := s.jobs[name]
	if !exists {
		return domain.ErrJobNotFound
	}
	job.Paused = paused
	s.jobs[name] = job
	return nil
}
func (s *stubScheduler) Job(name string) (domain.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, exists := s.jobs[name]
	return job, exists
}

func internalRequest(method, path, body, requestID, key string) *http.Request {
	req := httptest.NewRequest(method, path, stringsReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Request-Id", requestID)
	req.Header.Set("Idempotency-Key", key)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func stringsReader(value string) *strings.Reader { return strings.NewReader(value) }

func TestHandlerAuthenticatesAndReplaysIdempotentRegistration(t *testing.T) {
	service := newStubScheduler()
	handler := NewHTTPHandler(service, testToken, nil)
	path := JobsPathPrefix + "job"
	body := `{"schedule":"@every 1m","url":"http://service.test/command"}`

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, internalRequest(http.MethodPost, path, body, "req-first", "same-key"))
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, internalRequest(http.MethodPost, path, body, "req-replay", "same-key"))
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() || replay.Header().Get(requestIDHeader) != "req-first" {
		t.Fatalf("replay status=%d body=%s request_id=%q", replay.Code, replay.Body.String(), replay.Header().Get(requestIDHeader))
	}
	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, internalRequest(http.MethodPost, path, `{"schedule":"@every 2m","url":"http://service.test/command"}`, "req-conflict", "same-key"))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestHandlerRejectsNonCanonicalHeadersAndDuplicateJSONKeys(t *testing.T) {
	service := newStubScheduler()
	handler := NewHTTPHandler(service, testToken, nil)
	noncanonical := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, JobsPathPrefix+"job", stringsReader(`{"schedule":"@every 1m","url":"http://service.test"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Kokoro-Request-Id", "noncanonical")
	req.Header.Set("Idempotency-Key", "noncanonical-key")
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(noncanonical, req)
	if noncanonical.Code != http.StatusBadRequest {
		t.Fatalf("noncanonical header status=%d body=%s", noncanonical.Code, noncanonical.Body.String())
	}

	duplicate := httptest.NewRecorder()
	handler.ServeHTTP(duplicate, internalRequest(http.MethodPost, JobsPathPrefix+"duplicate", `{"schedule":"@every 1m","url":"http://service.test","url":"http://service.test/other"}`, "req-duplicate", "duplicate-key"))
	if duplicate.Code != http.StatusBadRequest {
		t.Fatalf("duplicate JSON status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
}

func TestHandlerHealthProbeIsUnauthenticated(t *testing.T) {
	response := httptest.NewRecorder()
	NewHTTPHandler(newStubScheduler(), testToken, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health status=%d", response.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("health payload=%s err=%v", response.Body.String(), err)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok || data["status"] != "ok" {
		t.Fatalf("health payload=%s", response.Body.String())
	}
}

func TestHandlerReadinessFailsClosedWhenDependencyIsUnavailable(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	request.Header.Set(requestIDHeader, "req-readiness")
	NewHTTPHandler(newStubScheduler(), testToken, func(context.Context) error {
		return errors.New("redis unavailable")
	}).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Meta struct {
			RequestID string `json:"request_id"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "scheduler_not_ready" || payload.Meta.RequestID != "req-readiness" {
		t.Fatalf("readiness payload=%s", response.Body.String())
	}
}
