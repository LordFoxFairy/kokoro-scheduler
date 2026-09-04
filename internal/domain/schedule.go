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
	"time"
)

const (
	DefaultMethod          = MethodPost
	DefaultTimezone        = "UTC"
	DefaultRetryAttempts   = 1
	DefaultRetryBackoff    = 1
	DefaultRetryMaxBackoff = 3600
	DefaultRetryMaxWindow  = 3600
	MaxRetryAttempts       = 10
	MaxRetryBackoffSeconds = 3600
	MaxRetryWindowSeconds  = 86400
	MaxCatchUpOccurrences  = 100
)

const (
	MethodPost Method = "POST"
	MethodPut  Method = "PUT"

	MisfireSkip           MisfirePolicy = "skip"
	MisfireFireOnce       MisfirePolicy = "fire_once"
	MisfireCatchUpBounded MisfirePolicy = "catch_up_bounded"

	OverlapAllow  OverlapPolicy = "allow"
	OverlapForbid OverlapPolicy = "forbid"

	ScheduleActive ScheduleStatus = "active"
	SchedulePaused ScheduleStatus = "paused"
)

var (
	ErrScheduleAlreadyExists = errors.New("scheduler schedule already exists")
	ErrScheduleNotFound      = errors.New("scheduler schedule not found")
	ErrClaimLost             = errors.New("scheduler persistence claim is no longer owned")
	scheduleNamePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

type Method string
type MisfirePolicy string
type OverlapPolicy string
type ScheduleStatus string

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
	if p.MaxBackoffSeconds < p.BackoffSeconds || p.MaxBackoffSeconds > MaxRetryBackoffSeconds {
		return errors.New("retry.max_backoff_seconds must be between retry.backoff_seconds and 3600")
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

type Schedule struct {
	ID             string
	TenantID       string
	Name           string
	Rule           string
	Timezone       string
	TargetURL      string
	Method         Method
	Payload        json.RawMessage
	Retry          RetryPolicy
	MisfirePolicy  MisfirePolicy
	CatchUpLimit   int
	OverlapPolicy  OverlapPolicy
	Status         ScheduleStatus
	NextDueAt      time.Time
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ClaimOwner     string
	ClaimExpiresAt time.Time
}

func (s Schedule) Normalized() (Schedule, error) {
	s.TenantID = strings.TrimSpace(s.TenantID)
	s.Name = strings.TrimSpace(s.Name)
	s.Rule = strings.TrimSpace(s.Rule)
	s.Timezone = strings.TrimSpace(s.Timezone)
	s.TargetURL = strings.TrimSpace(s.TargetURL)
	if s.Timezone == "" {
		s.Timezone = DefaultTimezone
	}
	if s.Method == "" {
		s.Method = DefaultMethod
	}
	if len(s.Payload) == 0 {
		s.Payload = json.RawMessage(`{}`)
	}
	s.Retry = s.Retry.withDefaults()
	if s.MisfirePolicy == "" {
		s.MisfirePolicy = MisfireFireOnce
	}
	if s.CatchUpLimit == 0 {
		s.CatchUpLimit = 1
	}
	if s.OverlapPolicy == "" {
		s.OverlapPolicy = OverlapForbid
	}
	if s.Status == "" {
		s.Status = ScheduleActive
	}
	if err := s.Validate(); err != nil {
		return Schedule{}, err
	}
	canonicalPayload, err := canonicalizeJSON(s.Payload)
	if err != nil {
		return Schedule{}, err
	}
	s.Payload = canonicalPayload
	s.NextDueAt = NormalizeInstant(s.NextDueAt)
	s.CreatedAt = NormalizeInstant(s.CreatedAt)
	s.UpdatedAt = NormalizeInstant(s.UpdatedAt)
	s.ClaimExpiresAt = NormalizeInstant(s.ClaimExpiresAt)
	return s, nil
}

func (s Schedule) Validate() error {
	if err := ValidateTenantID(s.TenantID); err != nil {
		return err
	}
	if !scheduleNamePattern.MatchString(s.Name) {
		return fmt.Errorf("schedule name %q is invalid", s.Name)
	}
	if s.Rule == "" || len(s.Rule) > 256 {
		return errors.New("schedule rule must contain between 1 and 256 characters")
	}
	if strings.HasPrefix(s.Rule, "TZ=") || strings.HasPrefix(s.Rule, "CRON_TZ=") {
		return errors.New("schedule timezone must use the timezone field")
	}
	if s.Timezone != "UTC" && !strings.Contains(s.Timezone, "/") {
		return errors.New("timezone must be UTC or an IANA area/location name")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("timezone %q is invalid: %w", s.Timezone, err)
	}
	if err := ValidateTargetURL(s.TargetURL); err != nil {
		return fmt.Errorf("schedule %q has invalid url: %w", s.Name, err)
	}
	if s.Method != MethodPost && s.Method != MethodPut {
		return fmt.Errorf("schedule %q has unsupported method %q", s.Name, s.Method)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(s.Payload, &payload); err != nil || payload == nil {
		return errors.New("payload must be a JSON object")
	}
	if err := s.Retry.Validate(); err != nil {
		return fmt.Errorf("schedule %q has invalid retry policy: %w", s.Name, err)
	}
	switch s.MisfirePolicy {
	case MisfireSkip, MisfireFireOnce:
		if s.CatchUpLimit != 1 {
			return errors.New("catch_up_limit must be 1 unless misfire_policy is catch_up_bounded")
		}
	case MisfireCatchUpBounded:
		if s.CatchUpLimit < 1 || s.CatchUpLimit > MaxCatchUpOccurrences {
			return fmt.Errorf("catch_up_limit must be between 1 and %d", MaxCatchUpOccurrences)
		}
	default:
		return fmt.Errorf("schedule %q has invalid misfire_policy %q", s.Name, s.MisfirePolicy)
	}
	if s.OverlapPolicy != OverlapAllow && s.OverlapPolicy != OverlapForbid {
		return fmt.Errorf("schedule %q has invalid overlap_policy %q", s.Name, s.OverlapPolicy)
	}
	if s.Status != ScheduleActive && s.Status != SchedulePaused {
		return fmt.Errorf("schedule %q has invalid status %q", s.Name, s.Status)
	}
	return nil
}

func ValidateTenantID(tenantID string) error {
	if tenantID == "" || len(tenantID) > 128 || strings.IndexFunc(tenantID, func(character rune) bool {
		return character < 0x20 || character == 0x7f
	}) >= 0 {
		return errors.New("trusted tenant id must contain between 1 and 128 visible characters")
	}
	return nil
}

func IsValidScheduleName(name string) bool { return scheduleNamePattern.MatchString(name) }

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
			return nil, errors.New("JSON payload contains trailing data")
		}
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(canonical), nil
}

func ValidateTargetURL(rawURL string) error { return validateTargetURL(rawURL, true) }

func ValidateTargetURLSyntax(rawURL string) error { return validateTargetURL(rawURL, false) }

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
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || strings.Contains(hostname, "%") {
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

func IsSafeTargetAddress(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
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
