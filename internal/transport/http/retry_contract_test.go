package transporthttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodePayloadAcceptsBoundedRetryAndMisfirePolicies(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, SchedulesPathPrefix+"job", strings.NewReader(`{
		"schedule":"@every 1m",
		"timezone":"UTC",
		"url":"http://service.test/command",
		"retry":{
			"max_attempts":4,
			"backoff_seconds":2,
			"max_backoff_seconds":10,
			"max_retry_window_seconds":60
		},
		"misfire_policy":"catch_up_bounded",
		"catch_up_limit":3,
		"overlap_policy":"forbid"
	}`))
	request.Header.Set("Content-Type", "application/json")
	schedule, _, err := decodePayload(request, "tenant-a", "job", "")
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Retry.MaxAttempts != 4 || schedule.Retry.BackoffSeconds != 2 || schedule.Retry.MaxBackoffSeconds != 10 || schedule.Retry.MaxRetryWindowSeconds != 60 {
		t.Fatalf("retry policy = %#v", schedule.Retry)
	}
	if schedule.MisfirePolicy != "catch_up_bounded" || schedule.CatchUpLimit != 3 || schedule.OverlapPolicy != "forbid" {
		t.Fatalf("recovery policy = %#v", schedule)
	}
}

func TestDecodePayloadRejectsZeroRetryBoundary(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, SchedulesPathPrefix+"job", strings.NewReader(`{
		"schedule":"@every 1m",
		"url":"http://service.test/command",
		"retry":{"max_attempts":2,"backoff_seconds":0}
	}`))
	request.Header.Set("Content-Type", "application/json")
	_, _, err := decodePayload(request, "tenant-a", "job", "")
	if err == nil || !strings.Contains(err.Error(), "backoff_seconds") {
		t.Fatalf("error = %v, want backoff_seconds boundary error", err)
	}
}

func TestDecodePayloadRejectsNullRetryBoundary(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, SchedulesPathPrefix+"job", strings.NewReader(`{
		"schedule":"@every 1m",
		"url":"http://service.test/command",
		"retry":{"max_attempts":2,"max_backoff_seconds":null}
	}`))
	request.Header.Set("Content-Type", "application/json")
	_, _, err := decodePayload(request, "tenant-a", "job", "")
	if err == nil || !strings.Contains(err.Error(), "max_backoff_seconds") {
		t.Fatalf("error = %v, want max_backoff_seconds null error", err)
	}
}

func TestDecodePayloadRejectsCatchUpLimitWithoutBoundedPolicy(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, SchedulesPathPrefix+"job", strings.NewReader(`{
		"schedule":"@every 1m",
		"url":"http://service.test/command",
		"misfire_policy":"fire_once",
		"catch_up_limit":2
	}`))
	request.Header.Set("Content-Type", "application/json")
	_, _, err := decodePayload(request, "tenant-a", "job", "")
	if err == nil || !strings.Contains(err.Error(), "catch_up_limit") {
		t.Fatalf("error = %v, want catch_up_limit boundary error", err)
	}
}

func TestDecodePayloadRejectsExplicitEmptyDefaultedFields(t *testing.T) {
	for _, field := range []string{"name", "timezone", "method", "misfire_policy", "overlap_policy"} {
		t.Run(field, func(t *testing.T) {
			body := `{"schedule":"@every 1m","url":"http://service.test/command","` + field + `":""}`
			request := httptest.NewRequest(http.MethodPost, SchedulesPathPrefix+"job", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			if _, _, err := decodePayload(request, "tenant-a", "job", ""); err == nil {
				t.Fatalf("explicit empty %s must not be treated as omission", field)
			}
		})
	}
}

func TestDecodePayloadRejectsExplicitZeroCatchUpLimit(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, SchedulesPathPrefix+"job", strings.NewReader(`{
		"schedule":"@every 1m",
		"url":"http://service.test/command",
		"catch_up_limit":0
	}`))
	request.Header.Set("Content-Type", "application/json")
	if _, _, err := decodePayload(request, "tenant-a", "job", ""); err == nil {
		t.Fatal("explicit zero catch_up_limit must not be replaced by the default")
	}
}
