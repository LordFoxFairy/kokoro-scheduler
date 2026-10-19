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
	mu                    sync.Mutex
	receipts              map[string]domain.CommandReceipt
	commands              []application.Command
	resultCodeByOperation map[domain.CommandOperation]string
}

func newStubCommandService() *stubCommandService {
	return &stubCommandService{
		receipts:              make(map[string]domain.CommandReceipt),
		resultCodeByOperation: make(map[domain.CommandOperation]string),
	}
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
		if !schedule.RunAt.IsZero() {
			schedule.NextDueAt = schedule.RunAt
		}
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
	if code, found := s.resultCodeByOperation[command.Operation]; found {
		result.Code = code
		result.Schedule = nil
	}
	receipt := domain.CommandReceipt{
		TenantID: command.TenantID, CommandScope: command.CommandScope,
		IdempotencyKey: command.IdempotencyKey, RequestDigest: command.RequestDigest,
		RequestID: command.RequestID, Result: result,
	}
	s.receipts[identity] = receipt
	return receipt, nil
}

func (s *stubCommandService) Get(context.Context, string, string) (domain.Schedule, error) {
	return domain.Schedule{}, domain.ErrScheduleNotFound
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
	// Stable digest from the pre-once recurring contract; adding run_at must not invalidate old receipts.
	if command.RequestDigest != "sha256:bd315f0a8fc6595210e77f61c11b0aeec49d034c1ebf009d24177936803ab513" {
		t.Fatalf("recurring receipt digest changed: %s", command.RequestDigest)
	}
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

func TestHandlerMapsStableControlResultsToCanonicalErrors(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		action     string
		resultCode string
		wantStatus int
		wantCode   string
		body       string
	}{
		{
			name: "duplicate create", method: http.MethodPost, resultCode: domain.ResultAlreadyExists,
			wantStatus: http.StatusConflict, wantCode: "schedule_already_exists",
			body: `{"schedule":"@every 1m","url":"http://service.test/command"}`,
		},
		{
			name: "missing replace", method: http.MethodPut, resultCode: domain.ResultNotFound,
			wantStatus: http.StatusNotFound, wantCode: "schedule_not_found",
			body: `{"schedule":"@every 1m","url":"http://service.test/command"}`,
		},
		{
			name: "missing delete", method: http.MethodDelete, resultCode: domain.ResultNotFound,
			wantStatus: http.StatusNotFound, wantCode: "schedule_not_found",
		},
		{name: "exhausted pause", method: http.MethodPost, action: "pause", resultCode: domain.ResultExhausted, wantStatus: 409, wantCode: "schedule_exhausted"},
		{name: "changed resume", method: http.MethodPost, action: "resume", resultCode: domain.ResultChanged, wantStatus: 409, wantCode: "schedule_changed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStubCommandService()
			service.resultCodeByOperation[operationFor(test.method, test.action)] = test.resultCode
			path := SchedulesPathPrefix + "billing.reconcile"
			if test.action != "" {
				path += "/" + test.action
			}
			response := httptest.NewRecorder()
			NewHTTPHandler(service, testToken, nil).ServeHTTP(response, internalRequest(
				test.method, path, test.body,
				"req-control-result", "control-result-key", "tenant-a",
			))
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != test.wantCode {
				t.Fatalf("error code=%q body=%s, want %q", envelope.Error.Code, response.Body.String(), test.wantCode)
			}
		})
	}
}

func TestHandlerPreservesLegalOpaqueIdempotencyKeyAtApplicationBoundary(t *testing.T) {
	service := newStubCommandService()
	key := "\u00a0AbC-._~:/?@!$&'()*+,;=[]{}#%\u00a0"
	server := httptest.NewServer(NewHTTPHandler(service, testToken, nil))
	defer server.Close()
	request, err := http.NewRequest(http.MethodDelete, server.URL+SchedulesPathPrefix+"billing.reconcile", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set(requestIDHeader, "req-opaque-key")
	request.Header.Set(idempotencyKeyHeader, key)
	request.Header.Set(tenantIDHeader, "tenant-a")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := service.lastCommand().IdempotencyKey; got != key {
		t.Fatalf("application idempotency key=%q, want byte-for-byte %q", got, key)
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

// A query must bypass Execute: command receipts remain frozen even when the row advances.
type currentScheduleService struct {
	*stubCommandService
	read func(context.Context, string, string) (domain.Schedule, error)
}

func (s *currentScheduleService) Get(ctx context.Context, tenant, name string) (domain.Schedule, error) {
	return s.read(ctx, tenant, name)
}

func TestHandlerReadsCurrentScheduleWithoutCommandIdentity(t *testing.T) {
	reads := 0
	service := &currentScheduleService{stubCommandService: newStubCommandService()}
	service.read = func(ctx context.Context, tenant, name string) (domain.Schedule, error) {
		reads++
		if tenant != "tenant-a" || name != "current" || ctx.Err() != nil {
			t.Fatalf("query context: tenant=%q name=%q err=%v", tenant, name, ctx.Err())
		}
		return domain.Schedule{ID: "schedule-id", Name: name, Status: domain.SchedulePaused,
			Rule: "@every 1m", Timezone: "UTC", Payload: []byte(`{}`), Version: 9,
			NextDueAt: time.Date(2026, 1, 1, 0, 6, 0, 0, time.UTC)}, nil
	}
	handler := NewHTTPHandler(service, testToken, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, internalRequest(http.MethodGet, SchedulesPathPrefix+"current", "", "req-current", "", "tenant-a"))
	if response.Code != http.StatusOK {
		t.Fatalf("query status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Data struct {
			Schedule struct {
				Status    string `json:"status"`
				Paused    bool   `json:"paused"`
				NextDueAt string `json:"next_due_at"`
				Version   int64  `json:"version"`
			} `json:"schedule"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	got := result.Data.Schedule
	if got.Status != "paused" || !got.Paused || got.NextDueAt != "2026-01-01T00:06:00Z" || got.Version != 9 {
		t.Fatalf("current row = %#v", got)
	}
	if response.Header().Get(requestIDHeader) != "req-current" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("query headers = %v", response.Header())
	}
	if reads != 1 || len(service.commands) != 0 || len(service.receipts) != 0 {
		t.Fatalf("reads=%d writes=%d receipts=%d", reads, len(service.commands), len(service.receipts))
	}
}

func TestHandlerCurrentScheduleValidatesBeforeReading(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, tenant, requestID, auth string
		want                                      int
	}{
		{"unauthenticated", "current", "", "tenant-a", "req-read", "", 401},
		{"tenant missing", "current", "", "", "req-read", "Bearer " + testToken, 400},
		{"request missing", "current", "", "tenant-a", "", "Bearer " + testToken, 400},
		{"query", "current?tenant_id=other", "", "tenant-a", "req-read", "Bearer " + testToken, 400},
		{"body", "current", `{}`, "tenant-a", "req-read", "Bearer " + testToken, 400},
		{"action", "current/pause", "", "tenant-a", "req-read", "Bearer " + testToken, 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &currentScheduleService{stubCommandService: newStubCommandService(), read: func(context.Context, string, string) (domain.Schedule, error) {
				t.Fatal("invalid query reached store")
				return domain.Schedule{}, nil
			}}
			request := internalRequest(http.MethodGet, SchedulesPathPrefix+tc.path, tc.body, tc.requestID, "", tc.tenant)
			request.Header.Set("Authorization", tc.auth)
			// Unknown-length bodies must not bypass validation either.
			request.ContentLength = -1
			response := httptest.NewRecorder()
			NewHTTPHandler(service, testToken, nil).ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("GET response may be cached")
			}
			if len(service.commands) != 0 {
				t.Fatal("query executed a command")
			}
		})
	}
}

func TestHandlerCurrentScheduleErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"missing or foreign", domain.ErrScheduleNotFound, 404, "schedule_not_found"},
		{"storage", errors.New("private storage diagnostic"), 500, "scheduler_read_failed"},
		{"cancelled", context.Canceled, 500, "scheduler_read_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			service := &currentScheduleService{stubCommandService: newStubCommandService(), read: func(got context.Context, _, _ string) (domain.Schedule, error) {
				if got.Err() != context.Canceled {
					t.Fatal("request cancellation was lost")
				}
				return domain.Schedule{}, tc.err
			}}
			response := httptest.NewRecorder()
			request := internalRequest(http.MethodGet, SchedulesPathPrefix+"current", "", "req-read", "", "tenant-a").WithContext(ctx)
			NewHTTPHandler(service, testToken, nil).ServeHTTP(response, request)
			if response.Code != tc.status || !strings.Contains(response.Body.String(), `"code":"`+tc.code+`"`) || strings.Contains(response.Body.String(), "private storage") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHandlerAcceptsExclusiveAbsoluteOnceTrigger(t *testing.T) {
	for _, tc := range []struct {
		name, trigger string
		want          int
	}{
		{"once", `"run_at":"2026-11-01T05:30:00.123Z"`, 200},
		{"both", `"schedule":"@daily","run_at":"2026-11-01T05:30:00Z"`, 400},
		{"neither", `"timezone":"UTC"`, 400},
		{"null", `"run_at":null`, 400},
		{"empty", `"run_at":""`, 400},
		{"local time", `"run_at":"2026-11-01T01:30:00"`, 400},
		{"offset", `"run_at":"2026-11-01T01:30:00-04:00"`, 400},
		{"short hour", `"run_at":"2026-11-01T5:30:00Z"`, 400},
		{"short hour and submillisecond", `"run_at":"2026-11-01T5:30:00.1234Z"`, 400},
		{"submillisecond", `"run_at":"2026-11-01T05:30:00.1234Z"`, 400},
		{"empty rule alongside once", `"schedule":"","run_at":"2026-11-01T05:30:00Z"`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newStubCommandService()
			response := httptest.NewRecorder()
			NewHTTPHandler(service, testToken, nil).ServeHTTP(response, internalRequest(http.MethodPost, SchedulesPathPrefix+"once", `{`+tc.trigger+`,"url":"http://service.test/command"}`, "req-once", "key-once", "tenant-a"))
			if response.Code != tc.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.want == 200 {
				if !strings.Contains(response.Body.String(), `"run_at":"2026-11-01T05:30:00.123Z"`) || strings.Contains(response.Body.String(), `"schedule":""`) {
					t.Fatalf("once wire=%s", response.Body.String())
				}
			} else if len(service.commands) != 0 {
				t.Fatal("invalid trigger reached application")
			}
		})
	}
}

func TestHandlerCurrentExhaustedHasNullNextNotEmptyTimestamp(t *testing.T) {
	service := &currentScheduleService{stubCommandService: newStubCommandService(), read: func(context.Context, string, string) (domain.Schedule, error) {
		s := domain.Schedule{ID: "once-id", Name: "once", Status: domain.ScheduleStatus("exhausted"), Payload: []byte(`{}`), Version: 2}
		if err := json.Unmarshal([]byte(`{"RunAt":"2026-11-01T05:30:00Z"}`), &s); err != nil {
			t.Fatal(err)
		}
		return s, nil
	}}
	response := httptest.NewRecorder()
	NewHTTPHandler(service, testToken, nil).ServeHTTP(response, internalRequest(http.MethodGet, SchedulesPathPrefix+"once", "", "req-exhausted", "", "tenant-a"))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"next_due_at":null`) || !strings.Contains(response.Body.String(), `"status":"exhausted"`) {
		t.Fatalf("exhausted wire=%s", response.Body.String())
	}
}
