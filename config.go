package scheduler

import "strings"

const TargetServiceTokenEnv = "SCHEDULER_TARGET_SERVICE_TOKEN"

// TargetServiceTokenFromEnv reads the optional credential used for outbound
// HTTP dispatches. A blank value preserves unauthenticated fixture dispatches.
func TargetServiceTokenFromEnv(getenv func(string) string) string {
	if getenv == nil {
		return ""
	}
	return strings.TrimSpace(getenv(TargetServiceTokenEnv))
}
