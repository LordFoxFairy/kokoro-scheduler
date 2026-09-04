package domain

import "testing"

func TestJobNormalizedRejectsUnsafeTargetURLs(t *testing.T) {
	for _, rawURL := range []string{
		"http://localhost/command",
		"http://localhost./command",
		"http://127.0.0.1/command",
		"http://0.0.0.0/command",
		"http://10.0.0.1/command",
		"http://172.16.0.1/command",
		"http://192.168.1.1/command",
		"http://169.254.169.254/command",
		"http://100.64.0.1/command",
		"http://192.0.2.1/command",
		"http://198.18.0.1/command",
		"http://198.51.100.1/command",
		"http://203.0.113.1/command",
		"http://240.0.0.1/command",
		"http://[::1]/command",
		"http://[::]/command",
		"http://[fc00::1]/command",
		"http://[fe80::1]/command",
		"http://[fe80::1%25en0]/command",
		"http://[2001:db8::1]/command",
		"ftp://service.test/command",
		"http://service.test:0/command",
		"http://service.test:65536/command",
		"http://service.test:abc/command",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := (Job{
				Name:     "job",
				Schedule: "@every 1m",
				URL:      rawURL,
				Body:     []byte(`{}`),
			}).Normalized()
			if err == nil {
				t.Fatalf("target URL %q must be rejected", rawURL)
			}
		})
	}
}

func TestJobNormalizedAcceptsServiceTestTarget(t *testing.T) {
	for _, rawURL := range []string{
		"http://service.test/command",
		"https://service.test:8443/command",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if _, err := (Job{
				Name:     "job",
				Schedule: "@every 1m",
				URL:      rawURL,
				Body:     []byte(`{}`),
			}).Normalized(); err != nil {
				t.Fatalf("service.test target URL %q should remain valid: %v", rawURL, err)
			}
		})
	}
}
