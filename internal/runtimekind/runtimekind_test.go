package runtimekind

import "testing"

func TestEveryDirectKindHasADisplayName(t *testing.T) {
	for kind, caps := range capabilityByKind {
		if caps.Direct && caps.DisplayName == "" {
			t.Fatalf("direct kind %q has no display name", kind)
		}
	}
}
