package config

import (
	"net/netip"
	"testing"
)

func TestLoadParsesStrictInternalTargetAllowlist(t *testing.T) {
	cfg, err := Load(func(name string) string {
		if name == InternalTargetAllowlistEnv {
			return `[{"host":"service.test","cidrs":["10.0.0.7/32","127.0.0.1/32"]}]`
		}
		return ""
	})
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
			_, err := Load(func(name string) string {
				if name == InternalTargetAllowlistEnv {
					return test.raw
				}
				return ""
			})
			if err == nil {
				t.Fatal("invalid target allowlist must be rejected")
			}
		})
	}
}
