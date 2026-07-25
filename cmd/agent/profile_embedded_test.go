//go:build !split

package main

import (
	"strings"
	"testing"

	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/internal/config"
)

func TestBuildProfileIsEmbedded(t *testing.T) {
	if buildProfile != "embedded" {
		t.Fatalf("build profile = %q, want embedded", buildProfile)
	}
}

func TestEmbeddedIdentityReaderUsesLocalService(t *testing.T) {
	service := identity.NewService(nil, nil)
	if got := provideEmbeddedChannelIdentityReader(service); got != service {
		t.Fatalf("identity reader = %T, want local service", got)
	}
}

func TestEmbeddedProfileRejectsInternalRPCSecret(t *testing.T) {
	if err := validateProfile(config.Config{
		InternalRPC: config.InternalRPCConfig{SharedSecret: " \t"},
	}); err != nil {
		t.Fatalf("blank secret: %v", err)
	}
	err := validateProfile(config.Config{
		InternalRPC: config.InternalRPCConfig{SharedSecret: "configured"},
	})
	if err == nil || !strings.Contains(err.Error(), "-tags split") {
		t.Fatalf("validation error = %v", err)
	}
}
