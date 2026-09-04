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
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/application"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

const testToken = "scheduler-internal-token"

type stubCommandService struct {
	mu       sync.Mutex
	receipts map[string]domain.CommandReceipt
	commands []application.Command
}

func newStubCommandService() *stubCommandService {
	return &stubCommandService{receipts: make(map[string]domain.CommandReceipt)}
}

func (s *stubCommandService) Execute(_ context.Context, command application.Command) (domain.CommandReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, command)
	identity := command.TenantID + "\x1f" + command.CommandScope + "\x1f" + command.IdempotencyKey
	if prior, found := s.receipts[identity]; found {
		if prior.RequestDigest != command.RequestDigest {
			return domain.CommandReceipt{}, application.ErrIdempotencyConflict
		}
		return prior, nil
	}
	result := domain.CommandResult{Name: command.Name}
	switch command.Operation {
	case domain.CommandCreate:
		result.Code = domain.ResultRegistered
		schedule := command.Schedule
		schedule.ID = "00000000-0000-0000-0000-000000000001"
		schedule.Version = 1
		schedule.NextDueAt = time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)
		result.Schedule = &schedule
	case domain.CommandUpdate:
		result.Code = domain.ResultUpdated
		schedule := command.Schedule
		result.Schedule = &schedule
	case domain.CommandDelete:
		result.Code = domain.ResultDeleted
	case domain.CommandPause:
		result.Code = domain.ResultPaused
	case domain.CommandResume:
		result.Code = domain.ResultResumed
	}
	receipt := domain.CommandReceipt{
		TenantID: command.TenantID, CommandScope: command.CommandScope,
		IdempotencyKey: command.IdempotencyKey, RequestDigest: command.RequestDigest,
		RequestID: command.RequestID, Result: result,
	}
	s.receipts[identity] = receipt
	return receipt, nil
}

func (s *stubCommandService) lastCommand() application.Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commands[len(s.commands)-1]
}

func internalRequest(method, path, body, requestID, key, tenantID string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(requestIDHeader, requestID)
	req.Header.Set(idempotencyKeyHeader, key)
	req.Header.Set(tenantIDHeader, tenantID)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func TestHandlerUsesDurableCommandReceiptAcrossHandlerReconstruction(t *testing.T) {
	service := newStubCommandService()
	path := SchedulesPathPrefix + "billing.reconcile"
	body := `{"schedule":"@every 1m","timezone":"America/New_York","url":"http://service.test/command","misfire_policy":"fire_once","overlap_policy":"forbid"}`

	first := httptest.NewRecorder()
	NewHTTPHandler(service, testToken, nil).ServeHTTP(first, internalRequest(http.MethodPost, path, body, "req-first", "same-key", "tenant-a"))
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	command := service.lastCommand()
	if command.TenantID != "tenant-a" || command.Name != "billing.reconcile" || command.Schedule.Timezone != "America/New_York" {
		t.Fatalf("application command = %#v", command)
	}

	replay := httptest.NewRecorder()
	NewHTTPHandler(service, testToken, nil).ServeHTTP(replay, internalRequest(http.MethodPost, path, body, "req-replay", "same-key", "tenant-a"))
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() || replay.Header().Get(requestIDHeader) != "req-first" {
		t.Fatalf("replay status=%d body=%s request_id=%q", replay.Code, replay.Body.String(), replay.Header().Get(requestIDHeader))
	}

	conflict := httptest.NewRecorder()
	NewHTTPHandler(service, testToken, nil).ServeHTTP(conflict, internalRequest(http.MethodPost, path, `{"schedule":"@every 2m","url":"http://service.test/command"}`, "req-conflict", "same-key", "tenant-a"))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestHandlerScopesSameCommandIdentityByTrustedTenant(t *testing.T) {
	service := newStubCommandService()
	path := SchedulesPathPrefix + "same-name"
	body := `{"schedule":"@every 1m","url":"http://service.test/command"}`
	for _, tenantID := range []string{"tenant-a", "tenant-b"} {
		response := httptest.NewRecorder()
		NewHTTPHandler(service, testToken, nil).ServeHTTP(response, internalRequest(http.MethodPost, path, body, "req-"+tenantID, "same-key", tenantID))
		if response.Code != http.StatusOK {
			t.Fatalf("tenant %s status=%d body=%s", tenantID, response.Code, response.Body.String())
		}
	}
	if got := len(service.receipts); got != 2 {
		t.Fatalf("tenant-scoped receipts = %d, want 2", got)
	}
}

func TestHandlerRejectsMissingTenantAndDuplicateJSONKeys(t *testing.T) {
	service := newStubCommandService()
	missingTenant := httptest.NewRecorder()
	NewHTTPHandler(service, testToken, nil).ServeHTTP(missingTenant, internalRequest(
		http.MethodPost, SchedulesPathPrefix+"job", `{"schedule":"@every 1m","url":"http://service.test"}`, "req-missing-tenant", "key", "",
	))
	if missingTenant.Code != http.StatusBadRequest {
		t.Fatalf("missing tenant status=%d body=%s", missingTenant.Code, missingTenant.Body.String())
	}

	duplicate := httptest.NewRecorder()
	NewHTTPHandler(service, testToken, nil).ServeHTTP(duplicate, internalRequest(
		http.MethodPost, SchedulesPathPrefix+"duplicate", `{"schedule":"@every 1m","url":"http://service.test","url":"http://service.test/other"}`, "req-duplicate", "duplicate-key", "tenant-a",
	))
	if duplicate.Code != http.StatusBadRequest {
		t.Fatalf("duplicate JSON status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
}

func TestHandlerDoesNotEchoInvalidRequestIdentity(t *testing.T) {
	request := internalRequest(http.MethodPost, SchedulesPathPrefix+"job", `{"schedule":"@every 1m","url":"http://service.test"}`, "invalid\x7frequest", "key", "tenant-a")
	response := httptest.NewRecorder()
	NewHTTPHandler(newStubCommandService(), testToken, nil).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid request identity status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get(requestIDHeader); got != "" {
		t.Fatalf("invalid request identity was echoed in response header: %q", got)
	}
	if strings.Contains(response.Body.String(), `invalid\u007frequest`) {
		t.Fatalf("invalid request identity was echoed in response body: %s", response.Body.String())
	}
}

func TestHandlerResponseExposesExplicitRecoveryPoliciesAndUTCInstant(t *testing.T) {
	handler := NewHTTPHandler(newStubCommandService(), testToken, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, internalRequest(
		http.MethodPost,
		SchedulesPathPrefix+"recover",
		`{"schedule":"0 9 * * MON-FRI","timezone":"America/New_York","url":"http://service.test/command","misfire_policy":"catch_up_bounded","catch_up_limit":3,"overlap_policy":"allow"}`,
		"req-policy", "policy-key", "tenant-a",
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Schedule struct {
				Timezone      string `json:"timezone"`
				MisfirePolicy string `json:"misfire_policy"`
				CatchUpLimit  int    `json:"catch_up_limit"`
				OverlapPolicy string `json:"overlap_policy"`
				NextDueAt     string `json:"next_due_at"`
			} `json:"schedule"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	got := envelope.Data.Schedule
	if got.Timezone != "America/New_York" || got.MisfirePolicy != "catch_up_bounded" || got.CatchUpLimit != 3 || got.OverlapPolicy != "allow" || got.NextDueAt != "2026-01-02T03:05:00Z" {
		t.Fatalf("schedule response = %#v", got)
	}
}

func TestHandlerHealthProbeIsUnauthenticated(t *testing.T) {
	response := httptest.NewRecorder()
	NewHTTPHandler(newStubCommandService(), testToken, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health status=%d", response.Code)
	}
}

func TestHandlerReadinessFailsClosedWhenDependencyIsUnavailable(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	request.Header.Set(requestIDHeader, "req-readiness")
	NewHTTPHandler(newStubCommandService(), testToken, func(context.Context) error {
		return errors.New("postgres unavailable")
	}).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status=%d body=%s", response.Code, response.Body.String())
	}
}

var _ CommandService = (*stubCommandService)(nil)
