package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
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
	Database                DatabaseTarget
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
	database, err := ParseDatabaseURL(getenv(DatabaseURLEnv))
	if err != nil {
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
		Database: database, RedisURL: redisURL, HTTPAddr: addr,
		InternalServiceToken:    strings.TrimSpace(getenv(InternalServiceTokenEnv)),
		TargetServiceToken:      strings.TrimSpace(getenv(TargetServiceTokenEnv)),
		InternalTargetAllowlist: allowlist,
		WakeupInterval:          wakeupInterval, DispatchTimeout: dispatchTimeout,
		ClaimTTL: claimTTL, BatchSize: batchSize, WorkerID: workerID,
	}, nil
}

// DatabaseTarget is created only by ParseDatabaseURL. Callers cannot mutate its
// driver URL and namespace independently.
type DatabaseTarget struct {
	driverURL  string
	schemaName string
}

func (target DatabaseTarget) DriverURL() string  { return target.driverURL }
func (target DatabaseTarget) SchemaName() string { return target.schemaName }

func ParseDatabaseURL(rawURL string) (DatabaseTarget, error) {
	if strings.IndexFunc(rawURL, unicode.IsControl) >= 0 {
		return DatabaseTarget{}, errors.New("database URL must not contain raw control characters")
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return DatabaseTarget{}, errors.New("value is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || strings.Trim(parsed.Path, "/") == "" || strings.Contains(rawURL, "#") {
		return DatabaseTarget{}, errors.New("value must be an absolute postgres or postgresql database URL without a fragment")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return DatabaseTarget{}, errors.New("database URL query must use valid encoding")
	}
	for key := range query {
		switch strings.ToLower(key) {
		case "schema":
			if key != "schema" {
				return DatabaseTarget{}, errors.New("database URL must use the exact schema selector key")
			}
		case "options", "search_path", "timezone":
			return DatabaseTarget{}, errors.New("database URL must not override namespace or timezone")
		}
	}
	schemas := query["schema"]
	if len(schemas) != 1 || len(schemas[0]) == 0 || len(schemas[0]) > 63 {
		return DatabaseTarget{}, errors.New("database URL requires exactly one nonempty schema selector of at most 63 ASCII bytes")
	}
	schemaName := schemas[0]
	for index, character := range []byte(schemaName) {
		if (character >= 'a' && character <= 'z') || (index > 0 && ((character >= '0' && character <= '9') || character == '_')) {
			continue
		}
		return DatabaseTarget{}, errors.New("database schema must be a lowercase ASCII identifier starting with a letter")
	}
	if schemaName == "public" || strings.HasPrefix(schemaName, "pg_") {
		return DatabaseTarget{}, errors.New("database schema must be an explicit nonreserved owner namespace")
	}
	query.Del("schema")
	query.Set("search_path", schemaName)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	return DatabaseTarget{driverURL: parsed.String(), schemaName: schemaName}, nil
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
