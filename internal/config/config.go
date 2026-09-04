package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DatabaseURLEnv             = "SCHEDULER_DATABASE_URL"
	RedisURLEnv                = "SCHEDULER_REDIS_URL"
	HTTPAddrEnv                = "SCHEDULER_HTTP_ADDR"
	InternalServiceTokenEnv    = "SCHEDULER_INTERNAL_SERVICE_TOKEN"
	TargetServiceTokenEnv      = "SCHEDULER_TARGET_SERVICE_TOKEN"
	InternalTargetAllowlistEnv = "SCHEDULER_INTERNAL_TARGET_ALLOWLIST"
	WakeupIntervalEnv          = "SCHEDULER_WAKEUP_INTERVAL"
	ClaimTTLEnv                = "SCHEDULER_CLAIM_TTL"
	DispatchTimeoutEnv         = "SCHEDULER_DISPATCH_TIMEOUT"
	BatchSizeEnv               = "SCHEDULER_BATCH_SIZE"
	WorkerIDEnv                = "SCHEDULER_WORKER_ID"
	defaultHTTPAddr            = ":8080"
	defaultWakeupInterval      = time.Second
	defaultClaimTTL            = 2 * time.Minute
	defaultDispatchTimeout     = 30 * time.Second
	defaultBatchSize           = 100
	maximumBatchSize           = 1000
	maximumConfiguredDuration  = 24 * time.Hour
)

type Config struct {
	DatabaseURL             string
	RedisURL                string
	HTTPAddr                string
	InternalServiceToken    string
	TargetServiceToken      string
	InternalTargetAllowlist *TargetAllowlist
	WakeupInterval          time.Duration
	DispatchTimeout         time.Duration
	ClaimTTL                time.Duration
	BatchSize               int
	WorkerID                string
}

func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	databaseURL := strings.TrimSpace(getenv(DatabaseURLEnv))
	if err := validatePostgresURL(databaseURL); err != nil {
		return Config{}, fmt.Errorf("load %s: %w", DatabaseURLEnv, err)
	}
	redisURL := strings.TrimSpace(getenv(RedisURLEnv))
	if redisURL != "" {
		if err := validateRedisDBSeven(redisURL); err != nil {
			return Config{}, fmt.Errorf("load %s: %w", RedisURLEnv, err)
		}
	}
	wakeupInterval, err := durationValue(getenv, WakeupIntervalEnv, defaultWakeupInterval)
	if err != nil {
		return Config{}, err
	}
	claimTTL, err := durationValue(getenv, ClaimTTLEnv, defaultClaimTTL)
	if err != nil {
		return Config{}, err
	}
	dispatchTimeout, err := durationValue(getenv, DispatchTimeoutEnv, defaultDispatchTimeout)
	if err != nil {
		return Config{}, err
	}
	if claimTTL <= dispatchTimeout {
		return Config{}, fmt.Errorf("load %s: value must be greater than %s", ClaimTTLEnv, DispatchTimeoutEnv)
	}
	batchSize, err := integerValue(getenv, BatchSizeEnv, defaultBatchSize, 1, maximumBatchSize)
	if err != nil {
		return Config{}, err
	}
	workerID := strings.TrimSpace(getenv(WorkerIDEnv))
	if len(workerID) > 128 || strings.IndexFunc(workerID, controlCharacter) >= 0 {
		return Config{}, fmt.Errorf("load %s: value must contain at most 128 visible characters", WorkerIDEnv)
	}
	addr := strings.TrimSpace(getenv(HTTPAddrEnv))
	if addr == "" {
		addr = defaultHTTPAddr
	}
	allowlist, err := ParseInternalTargetAllowlist(getenv(InternalTargetAllowlistEnv))
	if err != nil {
		return Config{}, err
	}
	return Config{
		DatabaseURL: databaseURL, RedisURL: redisURL, HTTPAddr: addr,
		InternalServiceToken:    strings.TrimSpace(getenv(InternalServiceTokenEnv)),
		TargetServiceToken:      strings.TrimSpace(getenv(TargetServiceTokenEnv)),
		InternalTargetAllowlist: allowlist,
		WakeupInterval:          wakeupInterval, DispatchTimeout: dispatchTimeout,
		ClaimTTL: claimTTL, BatchSize: batchSize, WorkerID: workerID,
	}, nil
}

func validatePostgresURL(rawURL string) error {
	if rawURL == "" {
		return errors.New("value is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || strings.Trim(parsed.Path, "/") == "" {
		return errors.New("value must be an absolute postgres or postgresql database URL")
	}
	return nil
}

func validateRedisDBSeven(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "redis" && parsed.Scheme != "rediss") || parsed.Host == "" || parsed.Path != "/7" {
		return errors.New("configured Redis URL must select logical DB 7")
	}
	return nil
}

func durationValue(getenv func(string) string, name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 || value > maximumConfiguredDuration {
		return 0, fmt.Errorf("load %s: value must be a duration between 1ns and %s", name, maximumConfiguredDuration)
	}
	return value, nil
}

func integerValue(getenv func(string) string, name string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("load %s: value must be an integer between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func controlCharacter(character rune) bool { return character < 0x20 || character == 0x7f }
