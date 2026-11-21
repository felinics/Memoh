package userenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runLegacyHome runs legacy_home.sh with an explicit old and new home, the
// way the workspace runs it with the passwd home and /data.
func runLegacyHome(t *testing.T, oldHome, newHome string) map[string][]string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-s", "--", oldHome, newHome) //nolint:gosec // the embedded script on test temp dirs
	cmd.Stdin = strings.NewReader(LegacyHomeScript)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy_home.sh: %v\n%s", err, out)
	}
	return LegacyHomeDecisions(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a test temp dir
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertLinkTo(t *testing.T, link, target string) {
	t.Helper()
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("%s is not a symlink: %v", link, err)
	}
	if got != target {
		t.Fatalf("%s -> %s, want %s", link, got, target)
	}
}

func TestLegacyHomeMovesWhatTheNewHomeLacksAndLeavesLinks(t *testing.T) {
	root := t.TempDir()
	oldHome, newHome := filepath.Join(root, "home", "user"), filepath.Join(root, "data")
	writeFile(t, filepath.Join(oldHome, ".dim", "auth.json"), "signed-in")
	writeFile(t, filepath.Join(oldHome, ".bashrc"), "export PATH=\"$HOME/.bun/bin:$PATH\"\n")
	writeFile(t, filepath.Join(oldHome, ".bun", "bin", "bun"), "bun")
	writeFile(t, filepath.Join(newHome, "AGENTS.md"), "workspace")

	got := runLegacyHome(t, oldHome, newHome)

	for _, rel := range []string{".dim", ".bashrc"} {
		assertLinkTo(t, filepath.Join(oldHome, rel), filepath.Join(newHome, rel))
	}
	if readFile(t, filepath.Join(newHome, ".dim", "auth.json")) != "signed-in" {
		t.Fatal("sign-in was not adopted")
	}
	// An absolute path through the old home still works.
	if readFile(t, filepath.Join(oldHome, ".bun", "bin", "bun")) != "bun" {
		t.Fatal("old absolute path no longer resolves")
	}
	if !slices.Contains(got["adopted"], ".dim") || !slices.Contains(got["adopted"], ".bashrc") {
		t.Fatalf("decisions = %v", got)
	}
	if readFile(t, filepath.Join(newHome, "AGENTS.md")) != "workspace" {
		t.Fatal("existing workspace file changed")
	}
}

func TestLegacyHomeNeverOverwritesTheNewHome(t *testing.T) {
	root := t.TempDir()
	oldHome, newHome := filepath.Join(root, "root"), filepath.Join(root, "data")
	writeFile(t, filepath.Join(oldHome, ".config", "tool", "settings.json"), "old")
	writeFile(t, filepath.Join(oldHome, ".config", "only-old", "a"), "a")
	writeFile(t, filepath.Join(oldHome, ".profile"), "same")
	writeFile(t, filepath.Join(newHome, ".config", "tool", "settings.json"), "new")
	writeFile(t, filepath.Join(newHome, ".profile"), "same")

	got := runLegacyHome(t, oldHome, newHome)

	if readFile(t, filepath.Join(newHome, ".config", "tool", "settings.json")) != "new" {
		t.Fatal("new home file was overwritten")
	}
	if readFile(t, filepath.Join(oldHome, ".config", "tool", "settings.json")) != "old" {
		t.Fatal("conflicting old file was not kept")
	}
	if !slices.Contains(got["kept"], ".config/tool/settings.json") {
		t.Fatalf("conflict not reported: %v", got)
	}
	// Directories on both sides merge entry by entry.
	assertLinkTo(t, filepath.Join(oldHome, ".config", "only-old"), filepath.Join(newHome, ".config", "only-old"))
	// Identical files become one.
	assertLinkTo(t, filepath.Join(oldHome, ".profile"), filepath.Join(newHome, ".profile"))
	// A kept conflict is reported once, not on every start.
	if again := runLegacyHome(t, oldHome, newHome); len(again) != 0 {
		t.Fatalf("second run made decisions: %v", again)
	}
}

func TestLegacyHomeLeavesCachesBehind(t *testing.T) {
	root := t.TempDir()
	oldHome, newHome := filepath.Join(root, "home", "user"), filepath.Join(root, "data")
	writeFile(t, filepath.Join(oldHome, ".cache", "big"), "cache")
	writeFile(t, filepath.Join(oldHome, ".npm", "_cacache", "index"), "cache")
	writeFile(t, filepath.Join(oldHome, ".npm", "_logs", "debug.log"), "log")
	writeFile(t, filepath.Join(oldHome, ".local", "share", "uv", "cache", "wheel"), "cache")
	writeFile(t, filepath.Join(oldHome, ".local", "bin", "dim"), "dim")
	if err := os.MkdirAll(newHome, 0o750); err != nil {
		t.Fatal(err)
	}

	runLegacyHome(t, oldHome, newHome)

	for _, rel := range []string{".cache", ".npm/_cacache", ".local/share/uv/cache"} {
		if _, err := os.Lstat(filepath.Join(newHome, rel)); !os.IsNotExist(err) {
			t.Fatalf("cache %s was adopted (err=%v)", rel, err)
		}
	}
	assertLinkTo(t, filepath.Join(oldHome, ".local", "bin"), filepath.Join(newHome, ".local", "bin"))
	assertLinkTo(t, filepath.Join(oldHome, ".npm", "_logs"), filepath.Join(newHome, ".npm", "_logs"))
}

func TestLegacyHomeIsIdempotentAndCopiesUserLinksAsLinks(t *testing.T) {
	root := t.TempDir()
	oldHome, newHome := filepath.Join(root, "home", "user"), filepath.Join(root, "data")
	writeFile(t, filepath.Join(oldHome, ".dim", "cli", "current", "dim"), "dim")
	if err := os.Symlink("../.dim/cli/current/dim", filepath.Join(oldHome, "dim-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newHome, 0o750); err != nil {
		t.Fatal(err)
	}

	runLegacyHome(t, oldHome, newHome)
	assertLinkTo(t, filepath.Join(newHome, "dim-link"), "../.dim/cli/current/dim")
	if again := runLegacyHome(t, oldHome, newHome); len(again) != 0 {
		t.Fatalf("second run made decisions: %v", again)
	}
}

func TestLegacyHomeDoesNothingWhenTheHomeIsAlreadyTheDataVolume(t *testing.T) {
	newHome := filepath.Join(t.TempDir(), "data")
	writeFile(t, filepath.Join(newHome, ".bashrc"), "x")
	if got := runLegacyHome(t, newHome, newHome); len(got) != 0 {
		t.Fatalf("decisions = %v", got)
	}
}
