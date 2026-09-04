package contract_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/httpclient"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/application"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	transporthttp "github.com/LordFoxFairy/kokoro-scheduler/internal/transport/http"
)

func TestOpenAPIControlOperationsHaveRuntimeRouteParity(t *testing.T) {
	document := loadOpenAPI(t)
	service := &parityService{}
	handler := transporthttp.NewHTTPHandler(service, "contract-token", nil)
	for path, item := range document.Paths {
		if path == "/healthz" || path == "/readyz" {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Errorf("GET %s runtime status=%d", path, response.Code)
			}
			continue
		}
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			if _, declared := item[strings.ToLower(method)]; !declared {
				continue
			}
			runtimePath := strings.Replace(path, "{name}", "contract.parity", 1)
			body := ""
			if !strings.HasSuffix(runtimePath, "/pause") && !strings.HasSuffix(runtimePath, "/resume") && method != http.MethodDelete {
				body = `{"schedule":"@every 1m","timezone":"UTC","url":"http://service.test/command","misfire_policy":"fire_once","overlap_policy":"forbid"}`
			}
			request := httptest.NewRequest(method, runtimePath, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer contract-token")
			request.Header.Set("X-Kokoro-Tenant-Id", "tenant-contract")
			request.Header.Set("X-Request-Id", "request-contract-"+strings.ToLower(method))
			request.Header.Set("Idempotency-Key", "key-contract-"+strings.ToLower(method)+runtimePath)
			if body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Errorf("%s %s runtime status=%d body=%s", method, runtimePath, response.Code, response.Body.String())
			}
		}
	}
	for _, command := range service.snapshot() {
		if command.TenantID != "tenant-contract" || command.Name != "contract.parity" {
			t.Errorf("runtime command does not match contract context: %#v", command)
		}
	}
}

func TestOpenAPIDispatchHeadersMatchHTTPAdapter(t *testing.T) {
	document := loadOpenAPI(t)
	post := object(t, document.Webhooks["scheduleOccurrenceDispatch"], "post")
	got := referencedParameterNames(t, document, post["parameters"])
	for _, header := range []string{
		httpclient.TenantHeader,
		httpclient.ScheduleHeader,
		httpclient.OccurrenceHeader,
		httpclient.RequestIDHeader,
		httpclient.IdempotencyHeader,
		"traceparent",
	} {
		if !contains(got, header) {
			t.Errorf("HTTP dispatch adapter header %s is missing from OpenAPI", header)
		}
	}
}

type parityService struct {
	mu       sync.Mutex
	commands []application.Command
}

func (s *parityService) Execute(_ context.Context, command application.Command) (domain.CommandReceipt, error) {
	s.mu.Lock()
	s.commands = append(s.commands, command)
	s.mu.Unlock()
	result := domain.CommandResult{Name: command.Name}
	switch command.Operation {
	case domain.CommandCreate:
		result.Code = domain.ResultRegistered
	case domain.CommandUpdate:
		result.Code = domain.ResultUpdated
	case domain.CommandDelete:
		result.Code = domain.ResultDeleted
	case domain.CommandPause:
		result.Code = domain.ResultPaused
	case domain.CommandResume:
		result.Code = domain.ResultResumed
	}
	if command.Operation == domain.CommandCreate || command.Operation == domain.CommandUpdate {
		schedule := command.Schedule
		schedule.ID = "00000000-0000-0000-0000-000000000001"
		schedule.Version = 1
		schedule.NextDueAt = time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
		result.Schedule = &schedule
	}
	return domain.CommandReceipt{RequestID: command.RequestID, Result: result}, nil
}

func (s *parityService) snapshot() []application.Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]application.Command(nil), s.commands...)
}
