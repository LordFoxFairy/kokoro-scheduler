package config

import (
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const testDatabaseURL = "postgresql://scheduler:secret@127.0.0.1:5432/kokoro_scheduler"
const testOwnerDatabaseURL = testDatabaseURL + "?schema=kokoro_scheduler"
const testDriverDatabaseURL = testDatabaseURL + "?search_path=kokoro_scheduler&timezone=UTC"

func TestLoadRequiresPostgresFactStore(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), DatabaseURLEnv) {
		t.Fatalf("error = %v, want required %s", err, DatabaseURLEnv)
	}
}

func TestLoadUsesDurableSchedulerDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{DatabaseURLEnv: testOwnerDatabaseURL}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.DriverURL() != testDriverDatabaseURL || cfg.HTTPAddr != ":8080" {
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
			_, err := Load(env(map[string]string{DatabaseURLEnv: testOwnerDatabaseURL, RedisURLEnv: rawURL}))
			if err == nil || !strings.Contains(err.Error(), "logical DB 7") {
				t.Fatalf("error = %v, want logical DB 7 rejection", err)
			}
		})
	}
	if _, err := Load(env(map[string]string{DatabaseURLEnv: testOwnerDatabaseURL, RedisURLEnv: "rediss://redis.example:6380/7"})); err != nil {
		t.Fatalf("DB 7 URL rejected: %v", err)
	}
}

func TestLoadParsesWorkerBounds(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		DatabaseURLEnv:     testOwnerDatabaseURL,
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
			_, loadErr := Load(env(map[string]string{DatabaseURLEnv: testOwnerDatabaseURL, name: value}))
			if loadErr == nil {
				t.Fatalf("%s=%q must be rejected", name, value)
			}
		})
	}
}

func TestLoadRequiresClaimTTLToOutliveDispatchTimeout(t *testing.T) {
	_, err := Load(env(map[string]string{
		DatabaseURLEnv:     testOwnerDatabaseURL,
		ClaimTTLEnv:        "10s",
		DispatchTimeoutEnv: "10s",
	}))
	if err == nil || !strings.Contains(err.Error(), ClaimTTLEnv) {
		t.Fatalf("error = %v, want claim/dispatch safety boundary", err)
	}
}

func TestLoadParsesStrictInternalTargetAllowlist(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		DatabaseURLEnv:             testOwnerDatabaseURL,
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
			_, err := Load(env(map[string]string{DatabaseURLEnv: testOwnerDatabaseURL, InternalTargetAllowlistEnv: test.raw}))
			if err == nil {
				t.Fatal("invalid target allowlist must be rejected")
			}
		})
	}
}

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestLoadRejectsInvalidDatabaseNamespaceInputs(t *testing.T) {
	validURL := testDatabaseURL + "?schema=kokoro_scheduler"
	tests := []struct {
		name string
		raw  string
	}{
		{name: "missing selector", raw: testDatabaseURL},
		{name: "empty selector", raw: testDatabaseURL + "?schema="},
		{name: "duplicate identical selector", raw: validURL + "&schema=kokoro_scheduler"},
		{name: "duplicate different selector", raw: validURL + "&schema=other_owner"},
		{name: "encoded duplicate selector key", raw: validURL + "&%73chema=other_owner"},
		{name: "case variant selector key", raw: testDatabaseURL + "?SCHEMA=kokoro_scheduler"},
		{name: "case variant duplicate selector key", raw: validURL + "&SCHEMA=other_owner"},
		{name: "schema list", raw: testDatabaseURL + "?schema=kokoro_scheduler,other_owner"},
		{name: "encoded schema list", raw: testDatabaseURL + "?schema=kokoro_scheduler%2Cother_owner"},
		{name: "public schema", raw: testDatabaseURL + "?schema=public"},
		{name: "reserved schema prefix", raw: testDatabaseURL + "?schema=pg_owner"},
		{name: "uppercase schema", raw: testDatabaseURL + "?schema=Kokoro_scheduler"},
		{name: "quoted schema", raw: testDatabaseURL + "?schema=%22kokoro_scheduler%22"},
		{name: "non ASCII schema", raw: testDatabaseURL + "?schema=kokoro_%E5%BF%83"},
		{name: "overlong schema", raw: testDatabaseURL + "?schema=" + strings.Repeat("a", 64)},
		{name: "options override", raw: validURL + "&options=-csearch_path%3Dother_owner"},
		{name: "encoded options key", raw: validURL + "&%6fptions=-csearch_path%3Dother_owner"},
		{name: "case variant options key", raw: validURL + "&OPTIONS=-csearch_path%3Dother_owner"},
		{name: "search path override", raw: validURL + "&search_path=other_owner"},
		{name: "encoded search path key", raw: validURL + "&%73earch_path=other_owner"},
		{name: "case variant search path key", raw: validURL + "&SEARCH_PATH=other_owner"},
		{name: "timezone override", raw: validURL + "&timezone=America%2FNew_York"},
		{name: "encoded timezone key", raw: validURL + "&%74imezone=America%2FNew_York"},
		{name: "case variant timezone key", raw: validURL + "&TimeZone=America%2FNew_York"},
		{name: "leading raw newline", raw: "\n" + validURL},
		{name: "trailing raw newline", raw: validURL + "\n"},
		{name: "leading raw tab", raw: "\t" + validURL},
		{name: "trailing raw tab", raw: validURL + "\t"},
		{name: "leading raw carriage return", raw: "\r" + validURL},
		{name: "trailing raw carriage return", raw: validURL + "\r"},
		{name: "leading raw Unicode control", raw: "\u0085" + validURL},
		{name: "raw NUL", raw: validURL + "\x00"},
		{name: "raw DEL", raw: validURL + "\x7f"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(env(map[string]string{DatabaseURLEnv: test.raw}))
			if err == nil {
				t.Error("invalid database namespace input must be rejected by Load")
				return
			}
			if strings.Contains(err.Error(), "secret") {
				t.Error("configuration diagnostic exposed a database credential")
			}
		})
	}
}

func TestLoadAcceptsDatabaseNamespaceWithTLS(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		DatabaseURLEnv: testDatabaseURL + "?schema=kokoro_scheduler&sslmode=require",
	}))
	if err != nil {
		t.Fatal("explicit owner namespace with TLS must be accepted")
	}
	if !strings.Contains(cfg.Database.DriverURL(), "sslmode=require") {
		t.Error("database configuration lost the requested TLS mode")
	}
}

func TestLoadNormalizesExplicitDatabaseNamespaceThroughDriver(t *testing.T) {
	for _, schemaName := range []string{"kokoro_scheduler", strings.Repeat("a", 63)} {
		t.Run(schemaName, func(t *testing.T) {
			cfg, err := Load(env(map[string]string{
				DatabaseURLEnv: "  " + testDatabaseURL + "?schema=" + schemaName + "&sslmode=require&application_name=scheduler-fixture  ",
			}))
			if err != nil {
				t.Fatal("valid explicit namespace configuration must load")
			}
			parsed, err := url.Parse(cfg.Database.DriverURL())
			if err != nil {
				t.Fatal("normalized driver URL must parse")
			}
			query := parsed.Query()
			if query.Has("schema") || query.Get("search_path") != schemaName || query.Get("timezone") != "UTC" {
				t.Error("driver URL must consume schema and exclusively select the owner namespace in UTC")
			}
			if query.Get("sslmode") != "require" || query.Get("application_name") != "scheduler-fixture" {
				t.Error("normalization must retain ordinary TLS and connection parameters")
			}
			poolConfig, err := pgxpool.ParseConfig(cfg.Database.DriverURL())
			if err != nil {
				t.Fatal("normalized URL must be accepted by the actual PostgreSQL driver")
			}
			if poolConfig.ConnConfig.RuntimeParams["search_path"] != schemaName || poolConfig.ConnConfig.RuntimeParams["timezone"] != "UTC" || poolConfig.ConnConfig.TLSConfig == nil {
				t.Error("actual driver configuration must select the owner namespace, UTC and TLS")
			}
			if _, retained := poolConfig.ConnConfig.RuntimeParams["schema"]; retained {
				t.Error("application selector must not leak into PostgreSQL runtime parameters")
			}
		})
	}
}
