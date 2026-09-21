package redact

import "testing"

func TestDiagnostic(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"url userinfo", "dial https://admin:s3cr3t@registry.example.com/v2/", "dial https://***:***@registry.example.com/v2/"},
		{"secret parameter", "GET /v2/token?token=abc123 failed", "GET /v2/token?token=*** failed"},
		{"case insensitive parameter", "Access_Token=abc123", "Access_Token=***"},
		{"plain message is untouched", "image quay.io/memoh/workspace:1 not found", "image quay.io/memoh/workspace:1 not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Diagnostic(tc.in); got != tc.want {
				t.Fatalf("Diagnostic(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
