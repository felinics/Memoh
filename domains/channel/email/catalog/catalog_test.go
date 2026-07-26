package catalog

import (
	"log/slog"
	"testing"

	emailpkg "github.com/memohai/memoh/domains/channel/email"
)

func TestRegisterDefaultsReplacesDescriptorsWithTransports(t *testing.T) {
	registry := emailpkg.NewDescriptorRegistry()
	RegisterDefaults(registry, slog.Default(), nil, nil)

	for _, name := range []emailpkg.ProviderName{
		emailpkg.ProviderGeneric,
		emailpkg.ProviderGmail,
		emailpkg.ProviderMailgun,
	} {
		if _, err := registry.GetSender(name); err != nil {
			t.Fatalf("sender %q is not registered: %v", name, err)
		}
		if _, err := registry.GetReceiver(name); err != nil {
			t.Fatalf("receiver %q is not registered: %v", name, err)
		}
	}
}
