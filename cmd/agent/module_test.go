package main

import (
	"testing"

	"go.uber.org/fx"

	"github.com/memohai/memoh/internal/config"
)

// TestFXOptionsValidate proves runtime configuration cannot change the
// composition selected by the build profile.
func TestFXOptionsValidate(t *testing.T) {
	for _, cfg := range []config.Config{
		{},
		{InternalRPC: config.InternalRPCConfig{SharedSecret: "validate-only"}},
	} {
		if err := fx.ValidateApp(optionsFor(cfg)); err != nil {
			t.Fatal(err)
		}
	}
}
