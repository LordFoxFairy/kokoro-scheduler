package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

type jobConfig struct {
	Name          string          `json:"name"`
	Schedule      string          `json:"schedule"`
	URL           string          `json:"url"`
	Method        string          `json:"method"`
	Body          json.RawMessage `json:"body"`
	Retry         *retryConfig    `json:"retry"`
	MisfirePolicy string          `json:"misfire_policy"`
	Paused        bool            `json:"paused"`
}

type retryConfig struct {
	MaxAttempts           *int `json:"max_attempts"`
	BackoffSeconds        *int `json:"backoff_seconds"`
	MaxBackoffSeconds     *int `json:"max_backoff_seconds"`
	MaxRetryWindowSeconds *int `json:"max_retry_window_seconds"`
}

func (c *retryConfig) toDomain() (domain.RetryPolicy, error) {
	policy := domain.DefaultRetryPolicy()
	if c == nil {
		return policy, nil
	}
	if c.MaxAttempts != nil {
		policy.MaxAttempts = *c.MaxAttempts
	}
	if c.BackoffSeconds != nil {
		policy.BackoffSeconds = *c.BackoffSeconds
	}
	if c.MaxBackoffSeconds != nil {
		policy.MaxBackoffSeconds = *c.MaxBackoffSeconds
	}
	if c.MaxRetryWindowSeconds != nil {
		policy.MaxRetryWindowSeconds = *c.MaxRetryWindowSeconds
	}
	if err := policy.Validate(); err != nil {
		return domain.RetryPolicy{}, err
	}
	return policy, nil
}

func LoadJobs(raw string) ([]domain.Job, error) {
	if len(bytes.TrimSpace([]byte(raw))) == 0 {
		return []domain.Job{}, nil
	}
	if err := ensureNoDuplicateJSONKeys([]byte(raw)); err != nil {
		return nil, fmt.Errorf("decode scheduler jobs: %w", err)
	}
	var configs []jobConfig
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configs); err != nil {
		return nil, fmt.Errorf("decode scheduler jobs: %w", err)
	}
	if configs == nil {
		return nil, errors.New("decode scheduler jobs: expected a JSON array")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("decode scheduler jobs: trailing JSON")
		}
		return nil, fmt.Errorf("decode scheduler jobs: %w", err)
	}
	jobs := make([]domain.Job, len(configs))
	seen := make(map[string]struct{}, len(configs))
	for i := range configs {
		retry, err := configs[i].Retry.toDomain()
		if err != nil {
			return nil, fmt.Errorf("job %d: %w", i, err)
		}
		jobs[i] = domain.Job{
			Name: configs[i].Name, Schedule: configs[i].Schedule, URL: configs[i].URL,
			Method: domain.Method(configs[i].Method), Body: configs[i].Body,
			Retry:         retry,
			MisfirePolicy: domain.MisfirePolicy(configs[i].MisfirePolicy), Paused: configs[i].Paused,
		}
		normalized, err := jobs[i].Normalized()
		if err != nil {
			return nil, fmt.Errorf("job %d: %w", i, err)
		}
		if _, exists := seen[normalized.Name]; exists {
			return nil, fmt.Errorf("duplicate job name %q", normalized.Name)
		}
		seen[normalized.Name] = struct{}{}
		jobs[i] = normalized
	}
	return jobs, nil
}
