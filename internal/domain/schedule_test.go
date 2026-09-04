package domain

import "testing"

func TestScheduleNormalizedRejectsUnsafeTargetURLs(t *testing.T) {
	for _, rawURL := range []string{
		"http://localhost/command",
		"http://127.0.0.1/command",
		"http://10.0.0.1/command",
		"http://169.254.169.254/command",
		"http://192.0.2.1/command",
		"http://[::1]/command",
		"http://[2001:db8::1]/command",
		"ftp://service.test/command",
		"http://service.test:0/command",
		"http://service.test:65536/command",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := (Schedule{
				TenantID: "tenant-a", Name: "job", Rule: "@every 1m",
				TargetURL: rawURL,
			}).Normalized()
			if err == nil {
				t.Fatalf("target URL %q must be rejected", rawURL)
			}
		})
	}
}

func TestScheduleNormalizedCanonicalizesPayload(t *testing.T) {
	schedule, err := (Schedule{
		TenantID: "tenant-a", Name: "job", Rule: "@every 1m",
		TargetURL: "https://service.test:8443/command", Payload: []byte(`{ "z": 1, "a": true }`),
	}).Normalized()
	if err != nil {
		t.Fatal(err)
	}
	if string(schedule.Payload) != `{"a":true,"z":1}` {
		t.Fatalf("canonical payload = %s", schedule.Payload)
	}
}
