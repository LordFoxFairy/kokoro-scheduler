package application

import (
	"strings"
	"testing"
)

func TestLoadJobsNormalizesDefaultsAndRejectsUnknownFields(t *testing.T) {
	jobs, err := LoadJobs(`[{"name":"billing.reconcile","schedule":"@every 1m","url":"http://service.test/command","body":{"tenant_id":"TENANT"}}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Method != "POST" || jobs[0].Retry.MaxAttempts != 1 || jobs[0].MisfirePolicy != "skip" {
		t.Fatalf("unexpected normalized job: %#v", jobs)
	}
	if _, err := LoadJobs(`[{"name":"job","schedule":"@every 1m","url":"http://service.test","unknown":true}]`); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
	if _, err := LoadJobs(`[{"name":"job","schedule":"@every 1m","url":"http://service.test","url":"http://other.test"}]`); err == nil {
		t.Fatal("duplicate fields must be rejected")
	}
	if _, err := LoadJobs(`[{"name":"job","schedule":"@every 1m","url":"http://service.test"},{"name":"job","schedule":"@every 2m","url":"http://service.test"}]`); err == nil {
		t.Fatal("duplicate names must be rejected")
	}
}

func TestLoadJobsTreatsBlankConfigurationAsEmpty(t *testing.T) {
	jobs, err := LoadJobs(" \n\t")
	if err != nil {
		t.Fatal(err)
	}
	if jobs == nil || len(jobs) != 0 {
		t.Fatalf("jobs = %#v, want non-nil empty list", jobs)
	}
}

func TestLoadJobsRejectsURLCredentialsAndFragments(t *testing.T) {
	for _, url := range []string{
		"http://user:password@service.test/command",
		"http://service.test/command#fragment",
	} {
		if _, err := LoadJobs(`[{"name":"job","schedule":"@every 1m","url":"` + url + `"}]`); err == nil {
			t.Fatalf("URL %q must be rejected", url)
		}
	}
}

func TestLoadJobsAcceptsBoundedRetryPolicy(t *testing.T) {
	jobs, err := LoadJobs(`[{
		"name":"job",
		"schedule":"@every 1m",
		"url":"http://service.test/command",
		"retry":{
			"max_attempts":4,
			"backoff_seconds":2,
			"max_backoff_seconds":10,
			"max_retry_window_seconds":60
		}
	}]`)
	if err != nil {
		t.Fatal(err)
	}
	retry := jobs[0].Retry
	if retry.MaxAttempts != 4 || retry.BackoffSeconds != 2 || retry.MaxBackoffSeconds != 10 || retry.MaxRetryWindowSeconds != 60 {
		t.Fatalf("retry policy = %#v", retry)
	}
}

func TestLoadJobsAppliesSafeRetryDefaults(t *testing.T) {
	jobs, err := LoadJobs(`[{"name":"job","schedule":"@every 1m","url":"http://service.test/command"}]`)
	if err != nil {
		t.Fatal(err)
	}
	retry := jobs[0].Retry
	if retry.MaxAttempts != 1 || retry.BackoffSeconds != 1 || retry.MaxBackoffSeconds != 3600 || retry.MaxRetryWindowSeconds != 3600 {
		t.Fatalf("retry defaults = %#v", retry)
	}
}

func TestLoadJobsRejectsRetryPolicyOutsideBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		retryJSON string
		wantField string
	}{
		{name: "zero attempts", retryJSON: `"max_attempts":0`, wantField: "max_attempts"},
		{name: "too many attempts", retryJSON: `"max_attempts":11`, wantField: "max_attempts"},
		{name: "zero initial backoff", retryJSON: `"backoff_seconds":0`, wantField: "backoff_seconds"},
		{name: "initial backoff too large", retryJSON: `"backoff_seconds":3601`, wantField: "backoff_seconds"},
		{name: "zero maximum backoff", retryJSON: `"max_backoff_seconds":0`, wantField: "max_backoff_seconds"},
		{name: "maximum backoff too large", retryJSON: `"max_backoff_seconds":3601`, wantField: "max_backoff_seconds"},
		{name: "zero retry window", retryJSON: `"max_retry_window_seconds":0`, wantField: "max_retry_window_seconds"},
		{name: "retry window too large", retryJSON: `"max_retry_window_seconds":86401`, wantField: "max_retry_window_seconds"},
		{name: "maximum below initial", retryJSON: `"backoff_seconds":10,"max_backoff_seconds":9`, wantField: "max_backoff_seconds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := `[{"name":"job","schedule":"@every 1m","url":"http://service.test/command","retry":{` + test.retryJSON + `}}]`
			_, err := LoadJobs(raw)
			if err == nil || !strings.Contains(err.Error(), test.wantField) {
				t.Fatalf("error = %v, want field %q", err, test.wantField)
			}
		})
	}
}
