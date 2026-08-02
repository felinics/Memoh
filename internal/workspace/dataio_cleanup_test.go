package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanWorkspaceACPSecretsInDirUsesRootScopedRemoval(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	defer func() { _ = root.Close() }()

	writeRootFile(t, root, ".codex/sessions/session.json", "secret")
	writeRootFile(t, root, ".codex/auth.json", "secret")
	writeRootFile(t, root, ".hermes/state.db-wal", "secret")
	writeRootFile(t, root, ".memoh-hermes/mcp-tokens/token.json", "secret")
	writeRootFile(t, root, ".codex/config.toml", "keep")
	writeRootFile(t, root, "preserved/sentinel", "keep")
	if err := root.Symlink("../preserved", ".codex/auth"); err != nil {
		t.Fatalf("create protected-directory symlink: %v", err)
	}

	if err := cleanWorkspaceACPSecretsInDir(rootDir); err != nil {
		t.Fatalf("clean workspace ACP secrets: %v", err)
	}

	for _, path := range []string{
		".codex/sessions",
		".codex/auth.json",
		".codex/auth",
		".hermes/state.db-wal",
		".memoh-hermes/mcp-tokens",
	} {
		if _, err := root.Lstat(filepath.FromSlash(path)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("protected path %q still exists or returned unexpected error: %v", path, err)
		}
	}
	for _, path := range []string{".codex/config.toml", "preserved/sentinel"} {
		if _, err := root.Stat(filepath.FromSlash(path)); err != nil {
			t.Errorf("preserved path %q: %v", path, err)
		}
	}
}

func writeRootFile(t *testing.T, root *os.Root, path, content string) {
	t.Helper()
	path = filepath.FromSlash(path)
	if err := root.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create parent for %q: %v", path, err)
	}
	if err := root.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
