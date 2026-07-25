package main

import (
	"testing"

	"go.uber.org/fx"

	"github.com/memohai/memoh/internal/config"
)

// TestFXOptionsValidate proves the channel boundary's dependency set is
// explicit and closed (spec §7.3 assembly-closure verification).
func TestFXOptionsValidate(t *testing.T) {
	if err := fx.ValidateApp(options(config.Config{})); err != nil {
		t.Fatal(err)
	}
}
