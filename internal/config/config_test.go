package config

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

const testDatabaseURL = "postgresql://scheduler:secret@127.0.0.1:5432/kokoro_scheduler"

func TestLoadRequiresPostgresFactStore(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), DatabaseURLEnv) {
		t.Fatalf("error = %v, want required %s", err, DatabaseURLEnv)
	}
}

func TestLoadUsesDurableSchedulerDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{DatabaseURLEnv: testDatabaseURL}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != testDatabaseURL || cfg.HTTPAddr != ":8080" {
		t.Fatalf("database/http config = %#v", cfg)
	}
	if cfg.WakeupInterval != time.Second || cfg.ClaimTTL != 2*time.Minute || cfg.DispatchTimeout != 30*time.Second || cfg.BatchSize != 100 {
		t.Fatalf("scheduler defaults = %#v", cfg)
	}
}

func TestLoadRequiresRedisLogicalDatabaseSevenWhenConfigured(t *testing.T) {
	for _, rawURL := range []string{
		"redis://127.0.0.1:6379",
		"redis://127.0.0.1:6379/0",
		"redis://127.0.0.1:6379/8",
		"http://127.0.0.1:6379/7",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := Load(env(map[string]string{DatabaseURLEnv: testDatabaseURL, RedisURLEnv: rawURL}))
			if err == nil || !strings.Contains(err.Error(), "logical DB 7") {
				t.Fatalf("error = %v, want logical DB 7 rejection", err)
			}
		})
	}
	if _, err := Load(env(map[string]string{DatabaseURLEnv: testDatabaseURL, RedisURLEnv: "rediss://redis.example:6380/7"})); err != nil {
		t.Fatalf("DB 7 URL rejected: %v", err)
	}
}

func TestLoadParsesWorkerBounds(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		DatabaseURLEnv:     testDatabaseURL,
		WakeupIntervalEnv:  "250ms",
		ClaimTTLEnv:        "45s",
		DispatchTimeoutEnv: "9s",
		BatchSizeEnv:       "25",
		WorkerIDEnv:        "scheduler-a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WakeupInterval != 250*time.Millisecond || cfg.ClaimTTL != 45*time.Second || cfg.DispatchTimeout != 9*time.Second || cfg.BatchSize != 25 || cfg.WorkerID != "scheduler-a" {
		t.Fatalf("parsed config = %#v", cfg)
	}
	for name, value := range map[string]string{WakeupIntervalEnv: "0s", ClaimTTLEnv: "nope", DispatchTimeoutEnv: "-1s", BatchSizeEnv: "1001"} {
		t.Run(name, func(t *testing.T) {
			_, loadErr := Load(env(map[string]string{DatabaseURLEnv: testDatabaseURL, name: value}))
			if loadErr == nil {
				t.Fatalf("%s=%q must be rejected", name, value)
			}
		})
	}
}

func TestLoadRequiresClaimTTLToOutliveDispatchTimeout(t *testing.T) {
	_, err := Load(env(map[string]string{
		DatabaseURLEnv:     testDatabaseURL,
		ClaimTTLEnv:        "10s",
		DispatchTimeoutEnv: "10s",
	}))
	if err == nil || !strings.Contains(err.Error(), ClaimTTLEnv) {
		t.Fatalf("error = %v, want claim/dispatch safety boundary", err)
	}
}

func TestLoadParsesStrictInternalTargetAllowlist(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		DatabaseURLEnv:             testDatabaseURL,
		InternalTargetAllowlistEnv: `[{"host":"service.test","cidrs":["10.0.0.7/32","127.0.0.1/32"]}]`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	allowlist := cfg.InternalTargetAllowlist
	if !allowlist.Allows("service.test", netip.MustParseAddr("10.0.0.7")) {
		t.Fatal("configured private address must be allowed for its exact host")
	}
	if !allowlist.Allows("service.test", netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("configured loopback address must be allowed for its exact host")
	}
	if allowlist.Allows("service.test", netip.MustParseAddr("10.0.0.8")) {
		t.Fatal("unlisted private address must remain rejected")
	}
	if allowlist.Allows("other.test", netip.MustParseAddr("10.0.0.7")) {
		t.Fatal("configured address must not be transferable to another host")
	}
}

func TestLoadRejectsNonInternalOrAmbiguousTargetAllowlist(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: `[{"host":"service.test","cidrs":["10.0.0.7/32"],"wildcard":true}]`},
		{name: "wildcard host", raw: `[{"host":"*.internal","cidrs":["10.0.0.0/8"]}]`},
		{name: "public cidr", raw: `[{"host":"service.test","cidrs":["192.0.2.0/24"]}]`},
		{name: "non canonical cidr", raw: `[{"host":"service.test","cidrs":["10.0.0.7/24"]}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(env(map[string]string{DatabaseURLEnv: testDatabaseURL, InternalTargetAllowlistEnv: test.raw}))
			if err == nil {
				t.Fatal("invalid target allowlist must be rejected")
			}
		})
	}
}

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}
