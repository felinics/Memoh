package client

import (
	"path/filepath"
	"testing"
)

func TestResolvePathUnderVirtualRoot(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty uses root", raw: "", want: "/data"},
		{name: "data alias", raw: "/data/app", want: "/data/app"},
		{name: "relative", raw: "app/src", want: "/data/app/src"},
		{name: "cleans path", raw: "/data/app/../src", want: "/data/src"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolvePathUnderVirtualRoot("/data", tc.raw)
			if err != nil {
				t.Fatalf("ResolvePathUnderVirtualRoot() error = %v", err)
			}
			if got != filepath.Clean(tc.want) {
				t.Fatalf("ResolvePathUnderVirtualRoot() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolvePathUnderVirtualRootRejectsEscape(t *testing.T) {
	for _, raw := range []string{"..", "../escape", "/tmp/outside", "/data/../../etc"} {
		if _, err := ResolvePathUnderVirtualRoot("/data", raw); err == nil {
			t.Fatalf("ResolvePathUnderVirtualRoot(%q) expected escape error", raw)
		}
	}
}
