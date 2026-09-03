package transporthttp

import (
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

func parseJobPath(path string) (name, action string, ok bool) {
	if !strings.HasPrefix(path, JobsPathPrefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, JobsPathPrefix), "/")
	if len(parts) == 1 && domain.IsValidName(parts[0]) {
		return parts[0], "", true
	}
	if len(parts) == 2 && domain.IsValidName(parts[0]) && (parts[1] == "pause" || parts[1] == "resume") {
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

func validateRequestID(requestID string) error {
	if requestID == "" {
		return errors.New("X-Request-Id is required")
	}
	if len(requestID) > 128 || strings.IndexFunc(requestID, func(character rune) bool { return character < 0x20 || character == 0x7f }) >= 0 {
		return errors.New("X-Request-Id is invalid")
	}
	return nil
}

func validateIdempotencyKey(key string) error {
	if key == "" {
		return errors.New("Idempotency-Key is required")
	}
	if len(key) > 256 || strings.ContainsAny(key, "\r\n") {
		return errors.New("Idempotency-Key is invalid")
	}
	return nil
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}
