package netbird

import (
	"path/filepath"
	"strings"
	"testing"

	netctl "github.com/felinics/memoh/internal/network"
)

func TestNativeDriverBuildSpecKeepsResolvConfOutsideMounts(t *testing.T) {
	root := t.TempDir()
	cfg := netctl.BotOverlayConfig{Enabled: true, Provider: "netbird", Config: map[string]any{}}
	driver := newNativeDriver(cfg, nil, root, nil)
	spec, err := driver.buildSpec(netctl.AttachmentRequest{BotID: "bot-1", Overlay: cfg})
	if err != nil {
		t.Fatalf("buildSpec returned error: %v", err)
	}
	if want := filepath.Join(root, "bot-1", "netbird-resolv.conf"); spec.ResolvConfPath != want {
		t.Fatalf("ResolvConfPath = %q, want %q", spec.ResolvConfPath, want)
	}
	// netbird writes /var/lib/netbird/resolv.conf as an unclean-shutdown
	// backup, which would overwrite a copy kept in its state directory.
	for _, m := range spec.Mounts {
		if rel, err := filepath.Rel(m.Source, spec.ResolvConfPath); err == nil && !strings.HasPrefix(rel, "..") {
			t.Fatalf("ResolvConfPath %q is reachable through mount %q", spec.ResolvConfPath, m.Destination)
		}
	}
}
