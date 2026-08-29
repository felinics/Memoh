package main

import (
	"testing"

	"go.uber.org/fx"

	"github.com/felinics/memoh/internal/config"
)

// TestFXOptionsValidate validates both deployment shapes: split (channel
// runtime behind the internal RPC) and embedded (pre-split all-in-one,
// no shared secret configured).
func TestFXOptionsValidate(t *testing.T) {
	cases := map[string]config.Config{
		"split":    {InternalRPC: config.InternalRPCConfig{SharedSecret: "validate-only"}},
		"embedded": {},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := fx.ValidateApp(optionsFor(cfg)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
