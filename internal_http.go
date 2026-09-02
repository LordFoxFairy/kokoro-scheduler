package scheduler

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
)

const (
	InternalJobsPathPrefix = "/internal/scheduler/v1/jobs/"
	InternalServiceToken   = "Authorization"
	InternalRequestID      = "X-Request-Id"
	maxInternalBodyBytes   = 1 << 20
)

type idempotencyReceipt struct {
	fingerprint string
	status      int
	body        []byte
}

// InternalHandler owns only the process-local command receipts needed to make
// registration retries deterministic. It is not a durable task registry.
type InternalHandler struct {
	service      *Service
	serviceToken string
	mu           sync.Mutex
	receipts     map[string]idempotencyReceipt
}

func NewInternalHandler(service *Service, serviceToken string) *InternalHandler {
	return &InternalHandler{
		service:      service,
		serviceToken: strings.TrimSpace(serviceToken),
		receipts:     make(map[string]idempotencyReceipt),
	}
}

// NewHTTPHandler returns the scheduler HTTP surface. Health and readiness are
// intentionally unauthenticated; all scheduler job commands require the
// internal service token.
func NewHTTPHandler(service *Service, serviceToken string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", probeHandler)
	mux.HandleFunc("/readyz", probeHandler)
	mux.Handle(InternalJobsPathPrefix, NewInternalHandler(service, serviceToken))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, errorResponse("route_not_found", "route not found", ""))
	})
	return mux
}

func probeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse("method_not_allowed", "method not allowed", ""))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "kokoro-scheduler"})
}

func (h *InternalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFrom(r)
	if requestID != "" {
		setResponseRequestID(w, requestID)
	}
	if !h.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, errorResponse("service_auth_failed", "scheduler service authentication failed", requestID))
		return
	}
	if requestID == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse("request_id_required", "X-Request-Id is required", requestID))
		return
	}
	if len(requestID) > 128 || strings.IndexFunc(requestID, func(character rune) bool {
		return character < 0x20 || character == 0x7f
	}) >= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse("request_id_invalid", "X-Request-Id is invalid", requestID))
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
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse("idempotency_key_required", "Idempotency-Key is required", requestID))
		return
	}
	if len(idempotencyKey) > 256 || strings.ContainsAny(idempotencyKey, "\r\n") {
		writeJSON(w, http.StatusBadRequest, errorResponse("invalid_idempotency_key", "Idempotency-Key is invalid", requestID))
		return
	}

	payload, fingerprintPayload, err := h.readCommandPayload(r, name, action)
	if err != nil {
		status, code, message := commandDecodeError(err)
		writeJSON(w, status, errorResponse(code, message, requestID))
		return
	}

	scope := r.Method + ":" + r.URL.Path + ":" + idempotencyKey
	fingerprint := fingerprint(scope, fingerprintPayload)
	h.mu.Lock()
	defer h.mu.Unlock()
	if prior, exists := h.receipts[scope]; exists {
		if prior.fingerprint != fingerprint {
			writeJSON(w, http.StatusConflict, errorResponse("idempotency_conflict", "Idempotency key already used with a different request payload", requestID))
			return
		}
		setResponseRequestID(w, responseRequestID(prior.body, requestID))
		writeRawJSON(w, prior.status, prior.body)
		return
	}

	status, response := h.execute(r.Method, name, action, payload, requestID)
	responseBody, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse("scheduler_response_invalid", "scheduler response could not be encoded", requestID))
		return
	}
	h.receipts[scope] = idempotencyReceipt{fingerprint: fingerprint, status: status, body: responseBody}
	writeRawJSON(w, status, responseBody)
}

func (h *InternalHandler) authorized(r *http.Request) bool {
	if h.serviceToken == "" {
		return false
	}
	authorization := strings.TrimSpace(r.Header.Get(InternalServiceToken))
	scheme, token, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(h.serviceToken)) == 1
}

func (h *InternalHandler) readCommandPayload(r *http.Request, name, action string) (Job, []byte, error) {
	body, err := readInternalBody(r)
	if err != nil {
		return Job{}, nil, err
	}
	if action != "" {
		if len(body) > 0 && !isJSONContentType(r.Header.Get("Content-Type")) {
			return Job{}, nil, errUnsupportedMediaType
		}
		if len(body) == 0 {
			return Job{}, []byte("{}"), nil
		}
		var object map[string]json.RawMessage
		if err := decodeSingleJSON(body, &object); err != nil || object == nil || len(object) != 0 {
			return Job{}, nil, errInvalidAction
		}
		return Job{}, []byte("{}"), nil
	}
	if r.Method == http.MethodDelete {
		if len(body) == 0 {
			return Job{}, []byte("{}"), nil
		}
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			return Job{}, nil, errUnsupportedMediaType
		}
		var object map[string]json.RawMessage
		if err := decodeSingleJSON(body, &object); err != nil || object == nil || len(object) != 0 {
			return Job{}, nil, errInvalidDelete
		}
		return Job{}, []byte("{}"), nil
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return Job{}, nil, errUnsupportedMediaType
	}
	job, err := decodeJob(body, name)
	if err != nil {
		return Job{}, nil, err
	}
	canonical, err := json.Marshal(job)
	if err != nil {
		return Job{}, nil, fmt.Errorf("marshal job: %w", err)
	}
	return job, canonical, nil
}

func (h *InternalHandler) execute(method, name, action string, payload Job, requestID string) (int, map[string]any) {
	if h.service == nil {
		return http.StatusInternalServerError, errorResponse("scheduler_unavailable", "scheduler service is unavailable", requestID)
	}
	if action != "" {
		var err error
		paused := action == "pause"
		if paused {
			err = h.service.Pause(name)
		} else {
			err = h.service.Resume(name)
		}
		if errors.Is(err, ErrJobNotFound) {
			return http.StatusNotFound, errorResponse("job_not_found", "scheduler job was not found", requestID)
		}
		if err != nil {
			return http.StatusInternalServerError, errorResponse("scheduler_command_failed", "scheduler command failed", requestID)
		}
		return http.StatusOK, map[string]any{
			"data": map[string]any{"name": name, "paused": paused},
			"meta": map[string]string{"request_id": requestID},
		}
	}

	switch method {
	case http.MethodPost:
		if err := h.service.Register(payload); err != nil {
			if errors.Is(err, ErrJobAlreadyExists) {
				return http.StatusConflict, errorResponse("job_already_exists", "scheduler job is already registered", requestID)
			}
			return http.StatusBadRequest, errorResponse("invalid_job", err.Error(), requestID)
		}
		return jobMutationResponse(h.service, name, "registered", requestID)
	case http.MethodPut:
		if err := h.service.Update(payload); err != nil {
			if errors.Is(err, ErrJobNotFound) {
				return http.StatusNotFound, errorResponse("job_not_found", "scheduler job was not found", requestID)
			}
			return http.StatusBadRequest, errorResponse("invalid_job", err.Error(), requestID)
		}
		return jobMutationResponse(h.service, name, "updated", requestID)
	case http.MethodDelete:
		if err := h.service.Delete(name); err != nil {
			if errors.Is(err, ErrJobNotFound) {
				return http.StatusNotFound, errorResponse("job_not_found", "scheduler job was not found", requestID)
			}
			return http.StatusInternalServerError, errorResponse("scheduler_command_failed", "scheduler command failed", requestID)
		}
		return http.StatusOK, map[string]any{
			"data": map[string]string{"name": name, "status": "deleted"},
			"meta": map[string]string{"request_id": requestID},
		}
	default:
		return http.StatusMethodNotAllowed, errorResponse("method_not_allowed", "method not allowed", requestID)
	}
}

func jobMutationResponse(service *Service, name, status, requestID string) (int, map[string]any) {
	job, exists := service.Job(name)
	if !exists {
		return http.StatusInternalServerError, errorResponse("scheduler_response_invalid", "registered scheduler job could not be read", requestID)
	}
	return http.StatusOK, map[string]any{
		"data": map[string]any{"job": job, "status": status},
		"meta": map[string]string{"request_id": requestID},
	}
}

var (
	errUnsupportedMediaType = errors.New("unsupported media type")
	errInvalidAction        = errors.New("action body must be an empty JSON object")
	errInvalidDelete        = errors.New("delete body must be an empty JSON object")
	errInvalidJob           = errors.New("invalid scheduler job")
)

func decodeJob(body []byte, pathName string) (Job, error) {
	var raw map[string]json.RawMessage
	if err := decodeSingleJSON(body, &raw); err != nil || raw == nil {
		if err != nil {
			return Job{}, fmt.Errorf("decode job: %w", err)
		}
		return Job{}, errInvalidJob
	}
	for key, value := range raw {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Job{}, fmt.Errorf("job field %q must not be null", key)
		}
	}
	if retry, ok := raw["retry"]; ok {
		var retryFields map[string]json.RawMessage
		if err := decodeSingleJSON(retry, &retryFields); err != nil || retryFields == nil {
			if err != nil {
				return Job{}, fmt.Errorf("decode retry: %w", err)
			}
			return Job{}, errors.New("retry must be a JSON object")
		}
		for key, value := range retryFields {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return Job{}, fmt.Errorf("retry field %q must not be null", key)
			}
		}
	}
	var job Job
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&job); err != nil {
		return Job{}, fmt.Errorf("decode job: %w", err)
	}
	if job.Name == "" {
		job.Name = pathName
	} else if job.Name != pathName {
		return Job{}, errors.New("job name must match the path")
	}
	if err := normalizeJob(&job); err != nil {
		return Job{}, err
	}
	return job, nil
}

func decodeSingleJSON(body []byte, target any) error {
	if err := validateJSONStructure(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func validateJSONStructure(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walkJSONValue(decoder); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim('}') {
				return errors.New("JSON object is not closed")
			}
		case '[':
			for decoder.More() {
				if err := walkJSONValue(decoder); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim(']') {
				return errors.New("JSON array is not closed")
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}
	return nil
}

func readInternalBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInternalBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxInternalBodyBytes {
		return nil, errors.New("request body too large")
	}
	return body, nil
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func commandDecodeError(err error) (int, string, string) {
	switch {
	case errors.Is(err, errUnsupportedMediaType):
		return http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json"
	case errors.Is(err, errInvalidAction), errors.Is(err, errInvalidDelete), errors.Is(err, errInvalidJob):
		return http.StatusBadRequest, "invalid_job", err.Error()
	case strings.Contains(err.Error(), "request body too large"):
		return http.StatusRequestEntityTooLarge, "request_body_too_large", "request body is too large"
	default:
		return http.StatusBadRequest, "invalid_job", err.Error()
	}
}

func parseJobPath(path string) (name, action string, ok bool) {
	if !strings.HasPrefix(path, InternalJobsPathPrefix) {
		return "", "", false
	}
	relative := strings.TrimPrefix(path, InternalJobsPathPrefix)
	parts := strings.Split(relative, "/")
	if len(parts) == 1 && jobNamePattern.MatchString(parts[0]) {
		return parts[0], "", true
	}
	if len(parts) == 2 && jobNamePattern.MatchString(parts[0]) && (parts[1] == "pause" || parts[1] == "resume") {
		return parts[0], parts[1], true
	}
	return "", "", false
}

func supportedMethod(method, action string) bool {
	if action != "" {
		return method == http.MethodPost
	}
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodDelete
}

func allowedMethods(action string) string {
	if action != "" {
		return http.MethodPost
	}
	return strings.Join([]string{http.MethodPost, http.MethodPut, http.MethodDelete}, ", ")
}

func requestIDFrom(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(InternalRequestID))
}

func fingerprint(scope string, payload []byte) string {
	digest := sha256.Sum256(append([]byte(scope+"\n"), payload...))
	return fmt.Sprintf("sha256:%x", digest[:])
}

func responseRequestID(body []byte, fallback string) string {
	var envelope struct {
		Meta struct {
			RequestID string `json:"request_id"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Meta.RequestID != "" {
		return envelope.Meta.RequestID
	}
	return fallback
}

func setResponseRequestID(w http.ResponseWriter, requestID string) {
	w.Header().Set(InternalRequestID, requestID)
}

func errorResponse(code, message, requestID string) map[string]any {
	return map[string]any{
		"error": map[string]string{"code": code, "message": message},
		"meta":  map[string]string{"request_id": requestID},
	}
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
