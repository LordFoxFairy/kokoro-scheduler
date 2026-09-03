package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/robfig/cron/v3"
)

const (
	DefaultMethod          = "POST"
	MethodPost             = "POST"
	MethodPut              = "PUT"
	MisfireSkip            = "skip"
	MisfireFireOnce        = "fire_once"
	DefaultRetryAttempts   = 1
	DefaultRetryBackoff    = 1
	DefaultRetryMaxBackoff = 3600
	DefaultRetryMaxWindow  = 3600
	MaxRetryAttempts       = 10
	MaxRetryBackoffSeconds = 3600
	MaxRetryWindowSeconds  = 86400
)

var (
	ErrJobAlreadyExists = errors.New("scheduler job already exists")
	ErrJobNotFound      = errors.New("scheduler job not found")
	jobNamePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

type Method string

type MisfirePolicy string

type RetryPolicy struct {
	MaxAttempts           int `json:"max_attempts"`
	BackoffSeconds        int `json:"backoff_seconds"`
	MaxBackoffSeconds     int `json:"max_backoff_seconds"`
	MaxRetryWindowSeconds int `json:"max_retry_window_seconds"`
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:           DefaultRetryAttempts,
		BackoffSeconds:        DefaultRetryBackoff,
		MaxBackoffSeconds:     DefaultRetryMaxBackoff,
		MaxRetryWindowSeconds: DefaultRetryMaxWindow,
	}
}

func (p RetryPolicy) Validate() error {
	if p.MaxAttempts < 1 || p.MaxAttempts > MaxRetryAttempts {
		return errors.New("retry.max_attempts must be between 1 and 10")
	}
	if p.BackoffSeconds < 1 || p.BackoffSeconds > MaxRetryBackoffSeconds {
		return errors.New("retry.backoff_seconds must be between 1 and 3600")
	}
	if p.MaxBackoffSeconds < 1 || p.MaxBackoffSeconds > MaxRetryBackoffSeconds {
		return errors.New("retry.max_backoff_seconds must be between 1 and 3600")
	}
	if p.MaxBackoffSeconds < p.BackoffSeconds {
		return errors.New("retry.max_backoff_seconds must be greater than or equal to retry.backoff_seconds")
	}
	if p.MaxRetryWindowSeconds < 1 || p.MaxRetryWindowSeconds > MaxRetryWindowSeconds {
		return errors.New("retry.max_retry_window_seconds must be between 1 and 86400")
	}
	return nil
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	defaults := DefaultRetryPolicy()
	if p.MaxAttempts == 0 {
		p.MaxAttempts = defaults.MaxAttempts
	}
	if p.BackoffSeconds == 0 {
		p.BackoffSeconds = defaults.BackoffSeconds
	}
	if p.MaxBackoffSeconds == 0 {
		p.MaxBackoffSeconds = defaults.MaxBackoffSeconds
	}
	if p.MaxRetryWindowSeconds == 0 {
		p.MaxRetryWindowSeconds = defaults.MaxRetryWindowSeconds
	}
	return p
}

type Job struct {
	Name          string          `json:"name"`
	Schedule      string          `json:"schedule"`
	URL           string          `json:"url"`
	Method        Method          `json:"method"`
	Body          json.RawMessage `json:"body"`
	Retry         RetryPolicy     `json:"retry"`
	MisfirePolicy MisfirePolicy   `json:"misfire_policy"`
	Paused        bool            `json:"paused"`
}

func (j Job) Validate() error {
	if strings.TrimSpace(j.Name) == "" || strings.TrimSpace(j.Schedule) == "" || strings.TrimSpace(j.URL) == "" {
		return errors.New("requires name, schedule and url")
	}
	if !jobNamePattern.MatchString(j.Name) {
		return fmt.Errorf("%q has invalid name", j.Name)
	}
	if _, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor).Parse(j.Schedule); err != nil {
		return fmt.Errorf("%q has invalid schedule: %w", j.Name, err)
	}
	parsedURL, err := url.Parse(j.URL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return fmt.Errorf("%q has invalid url", j.Name)
	}
	if parsedURL.User != nil || parsedURL.Fragment != "" {
		return fmt.Errorf("%q has invalid url credentials or fragment", j.Name)
	}
	if j.Method != MethodPost && j.Method != MethodPut {
		return fmt.Errorf("%q has unsupported method %q", j.Name, j.Method)
	}
	if len(j.Body) == 0 {
		return errors.New("body must be a JSON object")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(j.Body, &body); err != nil || body == nil {
		return errors.New("body must be a JSON object")
	}
	if err := j.Retry.Validate(); err != nil {
		return fmt.Errorf("%q has invalid retry policy: %w", j.Name, err)
	}
	if j.MisfirePolicy != MisfireSkip && j.MisfirePolicy != MisfireFireOnce {
		return fmt.Errorf("%q has invalid misfire_policy %q", j.Name, j.MisfirePolicy)
	}
	return nil
}

func (j Job) Normalized() (Job, error) {
	j.Name = strings.TrimSpace(j.Name)
	j.Schedule = strings.TrimSpace(j.Schedule)
	j.URL = strings.TrimSpace(j.URL)
	if j.Method == "" {
		j.Method = DefaultMethod
	}
	if len(j.Body) == 0 {
		j.Body = json.RawMessage(`{}`)
	}
	j.Retry = j.Retry.withDefaults()
	if j.MisfirePolicy == "" {
		j.MisfirePolicy = MisfireSkip
	}
	if err := j.Validate(); err != nil {
		return Job{}, err
	}
	canonicalBody, err := canonicalizeJSON(j.Body)
	if err != nil {
		return Job{}, err
	}
	j.Body = canonicalBody
	return j, nil
}

func canonicalizeJSON(raw []byte) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("JSON body contains trailing data")
		}
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(canonical), nil
}

func IsValidName(name string) bool { return jobNamePattern.MatchString(name) }
