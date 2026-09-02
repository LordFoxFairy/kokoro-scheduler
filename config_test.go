package scheduler

import "testing"

func TestTargetServiceTokenFromEnvTrimsOptionalValue(t *testing.T) {
	getenv := func(name string) string {
		if name == TargetServiceTokenEnv {
			return " target-service-token "
		}
		return ""
	}

	if got := TargetServiceTokenFromEnv(getenv); got != "target-service-token" {
		t.Fatalf("target service token = %q, want trimmed token", got)
	}
}

func TestTargetServiceTokenFromEnvKeepsFixtureCompatibilityWhenEmpty(t *testing.T) {
	if got := TargetServiceTokenFromEnv(func(string) string { return "" }); got != "" {
		t.Fatalf("target service token = %q, want empty", got)
	}
}
