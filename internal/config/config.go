package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/application"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

const (
	JobsEnv                    = "SCHEDULER_JOBS_JSON"
	RedisURLEnv                = "SCHEDULER_REDIS_URL"
	HTTPAddrEnv                = "SCHEDULER_HTTP_ADDR"
	InternalServiceTokenEnv    = "SCHEDULER_INTERNAL_SERVICE_TOKEN"
	TargetServiceTokenEnv      = "SCHEDULER_TARGET_SERVICE_TOKEN"
	InternalTargetAllowlistEnv = "SCHEDULER_INTERNAL_TARGET_ALLOWLIST"
)

type Config struct {
	Jobs                    []domain.Job
	RedisURL                string
	HTTPAddr                string
	InternalServiceToken    string
	TargetServiceToken      string
	InternalTargetAllowlist *TargetAllowlist
	DispatchTimeout         time.Duration
	LeaseTTL                time.Duration
}

func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	jobs, err := application.LoadJobs(getenv(JobsEnv))
	if err != nil {
		return Config{}, fmt.Errorf("load %s: %w", JobsEnv, err)
	}
	addr := strings.TrimSpace(getenv(HTTPAddrEnv))
	if addr == "" {
		addr = ":8080"
	}
	allowlist, err := ParseInternalTargetAllowlist(getenv(InternalTargetAllowlistEnv))
	if err != nil {
		return Config{}, err
	}
	return Config{
		Jobs:                    jobs,
		RedisURL:                strings.TrimSpace(getenv(RedisURLEnv)),
		HTTPAddr:                addr,
		InternalServiceToken:    strings.TrimSpace(getenv(InternalServiceTokenEnv)),
		TargetServiceToken:      strings.TrimSpace(getenv(TargetServiceTokenEnv)),
		InternalTargetAllowlist: allowlist,
		DispatchTimeout:         application.DefaultDispatchTimeout,
		LeaseTTL:                application.DefaultLeaseTTL,
	}, nil
}
