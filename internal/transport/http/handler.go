package transporthttp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/application"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

const (
	JobsPathPrefix       = "/internal/scheduler/v1/jobs/"
	maxRequestBodyBytes  = 1 << 20
	maxReceipts          = 10000
	authorizationHeader  = "Authorization"
	requestIDHeader      = "X-Request-Id"
	idempotencyKeyHeader = "Idempotency-Key"
	readinessTimeout     = 2 * time.Second
)

type ReadinessProbe func(context.Context) error

type Scheduler interface {
	Register(domain.Job) error
	Update(domain.Job) error
	Remove(string) error
	Pause(string) error
	Resume(string) error
	Job(string) (domain.Job, bool)
}

type receipt struct {
	fingerprint string
	status      int
	body        []byte
}

type Handler struct {
	scheduler    Scheduler
	serviceToken string
	mu           sync.Mutex
	receipts     map[string]receipt
}

func NewHandler(scheduler Scheduler, serviceToken string) *Handler {
	return &Handler{scheduler: scheduler, serviceToken: strings.TrimSpace(serviceToken), receipts: make(map[string]receipt)}
}

func NewHTTPHandler(scheduler Scheduler, serviceToken string, readiness ReadinessProbe) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		probe(w, r, nil)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		probe(w, r, readiness)
	})
	mux.Handle(JobsPathPrefix, NewHandler(scheduler, serviceToken))
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
	if requestID != "" {
		w.Header().Set(requestIDHeader, requestID)
	}
	if !h.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, errorResponse("service_auth_failed", "scheduler service authentication failed", requestID))
		return
	}
	if err := validateRequestID(requestID); err != nil {
		code := "request_id_invalid"
		if requestID == "" {
			code = "request_id_required"
		}
		writeJSON(w, http.StatusBadRequest, errorResponse(code, err.Error(), requestID))
		return
	}
	name, action, ok := parseJobPath(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse("route_not_found", "job route not found", requestID))
		return
	}
	if !supportedMethod(r.Method, action) {
		w.Header().Set("Allow", allowedMethods(action))
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse("method_not_allowed", "method not allowed", requestID))
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotencyKeyHeader))
	if err := validateIdempotencyKey(key); err != nil {
		code := "invalid_idempotency_key"
		if key == "" {
			code = "idempotency_key_required"
		}
		writeJSON(w, http.StatusBadRequest, errorResponse(code, err.Error(), requestID))
		return
	}

	job, fingerprintPayload, err := decodePayload(r, name, action)
	if err != nil {
		status, code, message := decodeError(err)
		writeJSON(w, status, errorResponse(code, message, requestID))
		return
	}
	scope := r.Method + ":" + r.URL.Path + ":" + key
	fingerprint := fingerprint(scope, fingerprintPayload)

	h.mu.Lock()
	defer h.mu.Unlock()
	if prior, exists := h.receipts[scope]; exists {
		if prior.fingerprint != fingerprint {
			writeJSON(w, http.StatusConflict, errorResponse("idempotency_conflict", "Idempotency key already used with a different request payload", requestID))
			return
		}
		w.Header().Set(requestIDHeader, responseRequestID(prior.body, requestID))
		writeRawJSON(w, prior.status, prior.body)
		return
	}

	status, response := h.execute(r.Method, name, action, job, requestID)
	body, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse("scheduler_response_invalid", "scheduler response could not be encoded", requestID))
		return
	}
	if len(h.receipts) >= maxReceipts {
		for oldScope := range h.receipts {
			delete(h.receipts, oldScope)
			break
		}
	}
	h.receipts[scope] = receipt{fingerprint: fingerprint, status: status, body: body}
	writeRawJSON(w, status, body)
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

func (h *Handler) execute(method, name, action string, job domain.Job, requestID string) (int, map[string]any) {
	if h.scheduler == nil {
		return http.StatusInternalServerError, errorResponse("scheduler_unavailable", "scheduler service is unavailable", requestID)
	}
	if action != "" {
		var err error
		paused := action == "pause"
		if paused {
			err = h.scheduler.Pause(name)
		} else {
			err = h.scheduler.Resume(name)
		}
		if errors.Is(err, domain.ErrJobNotFound) {
			return http.StatusNotFound, errorResponse("job_not_found", "scheduler job was not found", requestID)
		}
		if err != nil {
			return http.StatusInternalServerError, errorResponse("scheduler_command_failed", "scheduler command failed", requestID)
		}
		return http.StatusOK, map[string]any{"data": map[string]any{"name": name, "paused": paused}, "meta": map[string]string{"request_id": requestID}}
	}

	switch method {
	case http.MethodPost:
		if err := h.scheduler.Register(job); err != nil {
			if errors.Is(err, domain.ErrJobAlreadyExists) {
				return http.StatusConflict, errorResponse("job_already_exists", "scheduler job is already registered", requestID)
			}
			return http.StatusBadRequest, errorResponse("invalid_job", err.Error(), requestID)
		}
		return mutationResponse(h.scheduler, name, "registered", requestID)
	case http.MethodPut:
		if err := h.scheduler.Update(job); err != nil {
			if errors.Is(err, domain.ErrJobNotFound) {
				return http.StatusNotFound, errorResponse("job_not_found", "scheduler job was not found", requestID)
			}
			return http.StatusBadRequest, errorResponse("invalid_job", err.Error(), requestID)
		}
		return mutationResponse(h.scheduler, name, "updated", requestID)
	case http.MethodDelete:
		if err := h.scheduler.Remove(name); err != nil {
			if errors.Is(err, domain.ErrJobNotFound) {
				return http.StatusNotFound, errorResponse("job_not_found", "scheduler job was not found", requestID)
			}
			return http.StatusInternalServerError, errorResponse("scheduler_command_failed", "scheduler command failed", requestID)
		}
		return http.StatusOK, map[string]any{"data": map[string]string{"name": name, "status": "deleted"}, "meta": map[string]string{"request_id": requestID}}
	default:
		return http.StatusMethodNotAllowed, errorResponse("method_not_allowed", "method not allowed", requestID)
	}
}

func mutationResponse(scheduler Scheduler, name, status, requestID string) (int, map[string]any) {
	job, exists := scheduler.Job(name)
	if !exists {
		return http.StatusInternalServerError, errorResponse("scheduler_response_invalid", "registered scheduler job could not be read", requestID)
	}
	return http.StatusOK, map[string]any{"data": map[string]any{"job": toJobResponse(job), "status": status}, "meta": map[string]string{"request_id": requestID}}
}

var _ Scheduler = (*application.Scheduler)(nil)
