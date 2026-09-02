package scheduler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/robfig/cron/v3"
)

type Job struct {
	Name          string         `json:"name"`
	Schedule      string         `json:"schedule"`
	URL           string         `json:"url"`
	Method        string         `json:"method"`
	Body          map[string]any `json:"body"`
	Retry         RetryPolicy    `json:"retry"`
	MisfirePolicy string         `json:"misfire_policy"`
	Paused        bool           `json:"paused"`
}

type RetryPolicy struct {
	MaxAttempts    int `json:"max_attempts"`
	BackoffSeconds int `json:"backoff_seconds"`
}

const (
	MisfireSkip     = "skip"
	MisfireFireOnce = "fire_once"
)

var jobNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

var (
	ErrJobAlreadyExists = errors.New("scheduler job already exists")
	ErrJobNotFound      = errors.New("scheduler job not found")
)

func LoadJobs(raw string) ([]Job, error) {
	if strings.TrimSpace(raw) == "" {
		return []Job{}, nil
	}
	var jobs []Job
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&jobs); err != nil {
		return nil, fmt.Errorf("decode scheduler jobs: %w", err)
	}
	if jobs == nil {
		return nil, errors.New("decode scheduler jobs: expected a JSON array")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode scheduler jobs: trailing JSON")
		}
		return nil, fmt.Errorf("decode scheduler jobs: %w", err)
	}
	seenNames := make(map[string]struct{}, len(jobs))
	for i := range jobs {
		if err := normalizeJob(&jobs[i]); err != nil {
			return nil, fmt.Errorf("job %d: %w", i, err)
		}
		if _, exists := seenNames[jobs[i].Name]; exists {
			return nil, fmt.Errorf("duplicate job name %q", jobs[i].Name)
		}
		seenNames[jobs[i].Name] = struct{}{}
	}
	return jobs, nil
}

func normalizeJob(job *Job) error {
	if job.Name == "" || job.Schedule == "" || job.URL == "" {
		return errors.New("requires name, schedule and url")
	}
	if !jobNamePattern.MatchString(job.Name) {
		return fmt.Errorf("%q has invalid name", job.Name)
	}
	if _, err := cron.ParseStandard(job.Schedule); err != nil {
		return fmt.Errorf("%q has invalid schedule: %w", job.Name, err)
	}
	parsedURL, err := url.ParseRequestURI(job.URL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return fmt.Errorf("%q has invalid url", job.Name)
	}
	if job.Method == "" {
		job.Method = http.MethodPost
	}
	if job.Method != http.MethodPost && job.Method != http.MethodPut {
		return fmt.Errorf("%q has unsupported method %q", job.Name, job.Method)
	}
	if job.Body == nil {
		job.Body = map[string]any{}
	}
	if job.Retry.MaxAttempts == 0 {
		job.Retry.MaxAttempts = 1
	}
	if job.Retry.MaxAttempts < 1 || job.Retry.MaxAttempts > 10 {
		return fmt.Errorf("%q has invalid retry.max_attempts", job.Name)
	}
	if job.Retry.BackoffSeconds < 0 || job.Retry.BackoffSeconds > 3600 {
		return fmt.Errorf("%q has invalid retry.backoff_seconds", job.Name)
	}
	if job.MisfirePolicy == "" {
		job.MisfirePolicy = MisfireSkip
	}
	if job.MisfirePolicy != MisfireSkip && job.MisfirePolicy != MisfireFireOnce {
		return fmt.Errorf("%q has invalid misfire_policy %q", job.Name, job.MisfirePolicy)
	}
	return nil
}
