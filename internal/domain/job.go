package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
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
	if err := ValidateTargetURL(j.URL); err != nil {
		return fmt.Errorf("%q has invalid url: %w", j.Name, err)
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

// ValidateTargetURL validates the URL syntax that is safe to retain in a Job.
// DNS answers are checked separately by the outbound adapter immediately before
// dialing, where the selected address is also pinned into the request URL.
func ValidateTargetURL(rawURL string) error {
	return validateTargetURL(rawURL, true)
}

// ValidateTargetURLSyntax validates URL syntax without evaluating a literal
// address. The outbound adapter uses this before applying its resolver and
// address policy; Job validation uses ValidateTargetURL to reject unsafe
// literals before they enter the registry.
func ValidateTargetURLSyntax(rawURL string) error {
	return validateTargetURL(rawURL, false)
}

func validateTargetURL(rawURL string, rejectUnsafeLiteral bool) error {
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Host == "" {
		return errors.New("target must be an absolute HTTP(S) URL with a host")
	}
	if !strings.EqualFold(parsedURL.Scheme, "http") && !strings.EqualFold(parsedURL.Scheme, "https") {
		return errors.New("target scheme must be http or https")
	}
	if parsedURL.User != nil || parsedURL.Fragment != "" {
		return errors.New("target must not contain credentials or a fragment")
	}
	if strings.HasSuffix(parsedURL.Host, ":") {
		return errors.New("target port must be a number between 1 and 65535")
	}
	if port := parsedURL.Port(); port != "" {
		parsedPort, parseErr := strconv.Atoi(port)
		if parseErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return errors.New("target port must be a number between 1 and 65535")
		}
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsedURL.Hostname()), ".")
	if hostname == "" {
		return errors.New("target host must not be empty")
	}
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return errors.New("target localhost is not allowed")
	}
	if strings.Contains(hostname, "%") {
		return errors.New("target host is not allowed")
	}
	if _, parseErr := netip.ParseAddr(hostname); parseErr != nil && numericHostname(hostname) {
		return errors.New("target host is not allowed")
	}
	if rejectUnsafeLiteral {
		if address, parseErr := netip.ParseAddr(hostname); parseErr == nil && !IsSafeTargetAddress(address) {
			return errors.New("target IP address is not allowed")
		}
	}
	return nil
}

// IsSafeTargetAddress returns whether an IP address is suitable for an
// outbound dynamic target. It intentionally excludes special-use ranges even
// when the platform would route them as global unicast.
func IsSafeTargetAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	if address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsLoopback() || address.IsPrivate() || address.IsUnspecified() || address.IsLinkLocalUnicast() || address.IsMulticast() {
		return false
	}
	for _, prefix := range reservedTargetPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

var reservedTargetPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("3fff::/20"),
}

func numericHostname(hostname string) bool {
	if hostname == "" {
		return false
	}
	for _, character := range hostname {
		if (character < '0' || character > '9') && character != '.' {
			return false
		}
	}
	return true
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
