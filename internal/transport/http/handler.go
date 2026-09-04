package transporthttp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/application"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

const (
	SchedulesPathPrefix  = "/internal/scheduler/v1/schedules/"
	maxRequestBodyBytes  = 1 << 20
	authorizationHeader  = "Authorization"
	requestIDHeader      = "X-Request-Id"
	idempotencyKeyHeader = "Idempotency-Key"
	tenantIDHeader       = "X-Kokoro-Tenant-Id"
	readinessTimeout     = 2 * time.Second
)

type ReadinessProbe func(context.Context) error

type CommandService interface {
	Execute(context.Context, application.Command) (domain.CommandReceipt, error)
}

type Handler struct {
	service      CommandService
	serviceToken string
}

func NewHandler(service CommandService, serviceToken string) *Handler {
	return &Handler{service: service, serviceToken: strings.TrimSpace(serviceToken)}
}

func NewHTTPHandler(service CommandService, serviceToken string, readiness ReadinessProbe) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		probe(w, r, nil)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		probe(w, r, readiness)
	})
	mux.Handle(SchedulesPathPrefix, NewHandler(service, serviceToken))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, errorResponse("route_not_found", "route not found", ""))
	})
	return mux
}

func probe(w http.ResponseWriter, r *http.Request, readiness ReadinessProbe) {
	requestID := probeRequestID(r)
	w.Header().Set(requestIDHeader, requestID)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse("method_not_allowed", "method not allowed", requestID))
		return
	}
	if readiness != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		err := readiness(ctx)
		cancel()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse("scheduler_not_ready", "scheduler is not ready", requestID))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]string{"status": "ok", "service": "kokoro-scheduler"},
		"meta": map[string]string{"request_id": requestID},
	})
}

func probeRequestID(r *http.Request) string {
	if requestID := strings.TrimSpace(r.Header.Get(requestIDHeader)); validateRequestID(requestID) == nil {
		return requestID
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return "sched_probe_" + hex.EncodeToString(value[:])
	}
	return "sched_probe_unavailable"
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.Header.Get(requestIDHeader))
	requestIDErr := validateRequestID(requestID)
	responseRequestID := ""
	if requestIDErr == nil {
		responseRequestID = requestID
		w.Header().Set(requestIDHeader, requestID)
	}
	if !h.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, errorResponse("service_auth_failed", "scheduler service authentication failed", responseRequestID))
		return
	}
	if requestIDErr != nil {
		code := "request_id_invalid"
		if requestID == "" {
			code = "request_id_required"
		}
		writeJSON(w, http.StatusBadRequest, errorResponse(code, requestIDErr.Error(), ""))
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get(tenantIDHeader))
	if err := domain.ValidateTenantID(tenantID); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse("tenant_context_invalid", err.Error(), requestID))
		return
	}
	name, action, ok := parseSchedulePath(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse("route_not_found", "schedule route not found", requestID))
		return
	}
	if !supportedMethod(r.Method, action) {
		w.Header().Set("Allow", allowedMethods(action))
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse("method_not_allowed", "method not allowed", requestID))
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get(idempotencyKeyHeader))
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		code := "invalid_idempotency_key"
		if idempotencyKey == "" {
			code = "idempotency_key_required"
		}
		writeJSON(w, http.StatusBadRequest, errorResponse(code, err.Error(), requestID))
		return
	}

	schedule, payload, err := decodePayload(r, tenantID, name, action)
	if err != nil {
		status, code, message := decodeError(err)
		writeJSON(w, status, errorResponse(code, message, requestID))
		return
	}
	if h.service == nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse("scheduler_unavailable", "scheduler service is unavailable", requestID))
		return
	}
	scope := r.Method + ":" + r.URL.Path
	command := application.Command{
		Operation: operationFor(r.Method, action), TenantID: tenantID, Name: name,
		CommandScope: scope, IdempotencyKey: idempotencyKey,
		RequestDigest: fingerprint(scope, payload), RequestID: requestID, Schedule: schedule,
	}
	receipt, err := h.service.Execute(r.Context(), command)
	if err != nil {
		h.writeApplicationError(w, err, requestID)
		return
	}
	w.Header().Set(requestIDHeader, receipt.RequestID)
	status, response := receiptResponse(receipt)
	writeJSON(w, status, response)
}

func operationFor(method, action string) domain.CommandOperation {
	switch action {
	case "pause":
		return domain.CommandPause
	case "resume":
		return domain.CommandResume
	}
	switch method {
	case http.MethodPost:
		return domain.CommandCreate
	case http.MethodPut:
		return domain.CommandUpdate
	case http.MethodDelete:
		return domain.CommandDelete
	default:
		return ""
	}
}

func (h *Handler) writeApplicationError(w http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, application.ErrIdempotencyConflict):
		writeJSON(w, http.StatusConflict, errorResponse("idempotency_conflict", "Idempotency key already used with a different request payload", requestID))
	case errors.Is(err, application.ErrInvalidCommand):
		writeJSON(w, http.StatusBadRequest, errorResponse("invalid_schedule", "scheduler schedule or recurrence rule is invalid", requestID))
	default:
		writeJSON(w, http.StatusInternalServerError, errorResponse("scheduler_command_failed", "scheduler command failed", requestID))
	}
}

func (h *Handler) authorized(r *http.Request) bool {
	if h.serviceToken == "" {
		return false
	}
	scheme, token, found := strings.Cut(strings.TrimSpace(r.Header.Get(authorizationHeader)), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(h.serviceToken)) == 1
}

var _ CommandService = (*application.Service)(nil)
