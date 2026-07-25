package container

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfSourceInvalidArgument(t *testing.T) {
	if _, err := ResolveConfSource(""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestResolveConfSourceUsesPreferredResolvedWhenAvailable(t *testing.T) {
	dataDir := t.TempDir()
	preferredPath := filepath.Join(dataDir, "preferred-resolv.conf")
	if err := os.WriteFile(preferredPath, []byte("nameserver 9.9.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := resolveConfSource(dataDir, preferredPath)
	if err != nil {
		t.Fatalf("resolveConfSource returned error: %v", err)
	}
	if path != preferredPath {
		t.Fatalf("expected preferred path, got %q", path)
	}
}

func TestResolveConfSourceUsesSystemdResolvedWhenAvailable(t *testing.T) {
	if _, err := os.Stat(systemdResolvConf); errors.Is(err, os.ErrNotExist) {
		t.Skip("systemd-resolved config not available on this host")
	} else if err != nil {
		t.Fatalf("failed to stat %s: %v", systemdResolvConf, err)
	}

	path, err := ResolveConfSource(t.TempDir())
	if err != nil {
		t.Fatalf("ResolveConfSource returned error: %v", err)
	}
	if path != systemdResolvConf {
		t.Fatalf("expected systemd-resolved path, got %q", path)
	}
}

func TestResolveConfSourceFallbackCreatesReadableFile(t *testing.T) {
	dataDir := t.TempDir()
	path, err := resolveConfSource(dataDir, filepath.Join(dataDir, "missing-resolv.conf"))
	if err != nil {
		t.Fatalf("resolveConfSource returned error: %v", err)
	}
	if path != filepath.Join(dataDir, "resolv.conf") {
		t.Fatalf("expected fallback path, got %q", path)
	}
	//nolint:gosec // test reads a file it just created in a temp directory
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != fallbackResolv {
		t.Fatalf("unexpected fallback resolv.conf contents: %q", string(content))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != fallbackResolvPerm {
		t.Fatalf("expected mode %o, got %o", fallbackResolvPerm, info.Mode().Perm())
	}
}

func TestResolveConfSourceFallbackFixesExistingPermissions(t *testing.T) {
	dataDir := t.TempDir()
	fallbackPath := filepath.Join(dataDir, "resolv.conf")
	if err := os.WriteFile(fallbackPath, []byte(fallbackResolv), 0o600); err != nil {
		t.Fatalf("failed to seed fallback resolv.conf: %v", err)
	}

	path, err := resolveConfSource(dataDir, filepath.Join(dataDir, "missing-preferred-resolv.conf"))
	if err != nil {
		t.Fatalf("resolveConfSource returned error: %v", err)
	}
	if path != fallbackPath {
		t.Fatalf("expected existing fallback path, got %q", path)
	}

	info, err := os.Stat(fallbackPath)
	if err != nil {
		t.Fatalf("failed to stat fallback resolv.conf: %v", err)
	}
	if perm := info.Mode().Perm(); perm != fallbackResolvPerm {
		t.Fatalf("expected permissions %o, got %o", fallbackResolvPerm, perm)
	}
}

func TestTimezoneSpecWithTZ(t *testing.T) {
	t.Setenv("TZ", "Asia/Shanghai")
	mounts, env := TimezoneSpec()
	if len(mounts) != 0 {
		t.Fatalf("expected no mounts, got %v", mounts)
	}
	if len(env) != 1 || env[0] != "TZ=Asia/Shanghai" {
		t.Fatalf("unexpected env: %v", env)
	}
}

func TestTimezoneSpecWithoutTZ(t *testing.T) {
	t.Setenv("TZ", "")
	mounts, _ := TimezoneSpec()
	if len(mounts) != 0 {
		t.Fatalf("expected no mounts, got %d", len(mounts))
	}
}
