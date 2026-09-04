package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
)

// TargetAllowlist is an explicit host-to-internal-CIDR policy. A nil policy
// means the default global-unicast policy remains in effect; a configured
// policy is exact and permits only its declared host/address pairs.
type TargetAllowlist struct {
	entries map[string][]netip.Prefix
}

func (a *TargetAllowlist) Allows(host string, address netip.Addr) bool {
	if a == nil {
		return false
	}
	host = normalizeAllowlistHostForLookup(host)
	address = address.Unmap()
	for _, prefix := range a.entries[host] {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

type targetAllowlistEntry struct {
	Host  string   `json:"host"`
	CIDRs []string `json:"cidrs"`
}

// ParseInternalTargetAllowlist parses the deployment-owned allowlist. Entries
// must use exact DNS hostnames and canonical CIDRs contained in RFC1918,
// loopback, or IPv6 ULA space. Public or special-use ranges are not accepted
// as an override by this policy parser.
func ParseInternalTargetAllowlist(raw string) (*TargetAllowlist, error) {
	if len(bytes.TrimSpace([]byte(raw))) == 0 {
		return nil, nil
	}
	if err := ensureNoDuplicateJSONKeys([]byte(raw)); err != nil {
		return nil, fmt.Errorf("decode %s: %w", InternalTargetAllowlistEnv, err)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var entries []targetAllowlistEntry
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode %s: %w", InternalTargetAllowlistEnv, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode %s: trailing JSON", InternalTargetAllowlistEnv)
		}
		return nil, fmt.Errorf("decode %s: %w", InternalTargetAllowlistEnv, err)
	}
	if entries == nil {
		return nil, fmt.Errorf("decode %s: expected a JSON array", InternalTargetAllowlistEnv)
	}

	allowlist := &TargetAllowlist{entries: make(map[string][]netip.Prefix, len(entries))}
	for index, entry := range entries {
		host, err := normalizeAllowlistHost(entry.Host)
		if err != nil {
			return nil, fmt.Errorf("%s entry %d: %w", InternalTargetAllowlistEnv, index, err)
		}
		if _, exists := allowlist.entries[host]; exists {
			return nil, fmt.Errorf("%s entry %d: duplicate host %q", InternalTargetAllowlistEnv, index, host)
		}
		if len(entry.CIDRs) == 0 {
			return nil, fmt.Errorf("%s entry %d: cidrs must contain at least one internal CIDR", InternalTargetAllowlistEnv, index)
		}
		prefixes := make([]netip.Prefix, 0, len(entry.CIDRs))
		seenPrefixes := make(map[netip.Prefix]struct{}, len(entry.CIDRs))
		for cidrIndex, rawCIDR := range entry.CIDRs {
			prefix, parseErr := netip.ParsePrefix(rawCIDR)
			if parseErr != nil || prefix != prefix.Masked() || !isInternalTargetPrefix(prefix) {
				return nil, fmt.Errorf("%s entry %d cidrs[%d]: must be a canonical private, loopback, or IPv6 ULA CIDR", InternalTargetAllowlistEnv, index, cidrIndex)
			}
			if _, exists := seenPrefixes[prefix]; exists {
				return nil, fmt.Errorf("%s entry %d: duplicate CIDR %q", InternalTargetAllowlistEnv, index, rawCIDR)
			}
			seenPrefixes[prefix] = struct{}{}
			prefixes = append(prefixes, prefix)
		}
		allowlist.entries[host] = prefixes
	}
	return allowlist, nil
}

func normalizeAllowlistHost(host string) (string, error) {
	if host == "" || strings.TrimSpace(host) != host {
		return "", errors.New("host must be a non-empty DNS hostname without surrounding whitespace")
	}
	normalized := normalizeAllowlistHostForLookup(host)
	if normalized == "" || len(normalized) > 253 || strings.Contains(normalized, "*") {
		return "", errors.New("host must be an exact DNS hostname; wildcards are not allowed")
	}
	if _, err := netip.ParseAddr(normalized); err == nil {
		return "", errors.New("host must be a DNS hostname, not an IP literal")
	}
	for _, label := range strings.Split(normalized, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("host %q is not a valid DNS hostname", host)
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return "", fmt.Errorf("host %q is not a valid DNS hostname", host)
			}
		}
	}
	return normalized, nil
}

func normalizeAllowlistHostForLookup(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func isInternalTargetPrefix(prefix netip.Prefix) bool {
	address := prefix.Addr().Unmap()
	for _, allowed := range internalTargetPrefixes {
		if address.Is4() == allowed.Addr().Is4() && prefix.Bits() >= allowed.Bits() && allowed.Contains(address) {
			return true
		}
	}
	return false
}

var internalTargetPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("::1/128"),
}

func ensureNoDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := walkJSON(decoder); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func walkJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}
