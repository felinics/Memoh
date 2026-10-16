package telemetry

import "testing"

func TestSafeEndpointRedactsCredentialsWithOrWithoutAScheme(t *testing.T) {
	// A bare host:port is a valid thing to configure, and url.Parse does not
	// recognise it as a URL — so the no-scheme forms are exactly the ones
	// that fall through unredacted if this only handles proper URLs.
	for _, tc := range []struct{ name, in, want string }{
		{"scheme with credentials", "http://user:pass@collector:4317", "http://collector:4317"},
		{"scheme with query", "https://host/v1/traces?token=secret", "https://host/v1/traces"},
		{"no scheme with credentials", "user:pass@collector:4317", "collector:4317"},
		{"no scheme with query", "collector:4317?token=secret", "collector:4317"},
		{"bare host and port", "collector:4317", "collector:4317"},
		{"ipv6", "[::1]:4317", "[::1]:4317"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeEndpoint(tc.in); got != tc.want {
				t.Errorf("safeEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSafeEndpointNeverKeepsAnythingBeforeAnAt(t *testing.T) {
	// The catch-all: whatever shape an endpoint takes, the part that can be a
	// credential is the part before the @.
	for _, in := range []string{
		"user:pass@host", "::::@host:1", "user:pass@", "grpc://u:p@h:1/x?y=z",
	} {
		if got := safeEndpoint(in); contains(got, "pass") || contains(got, ":p@") {
			t.Errorf("safeEndpoint(%q) = %q, still carries a credential", in, got)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
