package client

import (
	"testing"
)

func TestResolvePathUnderVirtualRootRejectsEscape(t *testing.T) {
	for _, raw := range []string{"..", "../escape", "/tmp/outside", "/data/../../etc"} {
		if _, err := ResolvePathUnderVirtualRoot("/data", raw); err == nil {
			t.Fatalf("ResolvePathUnderVirtualRoot(%q) expected escape error", raw)
		}
	}
}
