package userenv

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestEnvSetsEveryOwnedNameOnce(t *testing.T) {
	var names []string
	for _, entry := range Env() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("entry %q is not KEY=value", entry)
		}
		names = append(names, name)
	}
	if !slices.Equal(names, Names) {
		t.Fatalf("Env names = %q, want Names %q", names, Names)
	}
}

func TestEverythingTheUserKeepsIsUnderHome(t *testing.T) {
	for _, dir := range []string{ConfigDir, DataDir, CacheDir, BinDir, NPMPrefix, DepsBinDir} {
		if !strings.HasPrefix(dir, Home+"/") {
			t.Fatalf("%q is outside the persistent home %q", dir, Home)
		}
	}
	// npm -g puts commands in <prefix>/bin, which must be on Path.
	if NPMPrefix+"/bin" != BinDir {
		t.Fatalf("npm -g installs into %q/bin, not BinDir %q", NPMPrefix, BinDir)
	}
}

func TestPathOrder(t *testing.T) {
	dirs := strings.Split(Path, ":")
	index := func(dir string) int { return slices.Index(dirs, dir) }
	if index(DepsBinDir) != 0 {
		t.Fatalf("managed dependency shims must come first: %q", Path)
	}
	if index(BinDir) <= index(DepsBinDir) || index(BinDir) >= index(ToolkitBinDir) {
		t.Fatalf("user commands must sit between managed shims and the toolkit: %q", Path)
	}
	if index("/usr/bin") < index(ToolkitBinDir) {
		t.Fatalf("the toolkit must precede system directories: %q", Path)
	}
}

// The workspace image declares the same environment, for processes that do
// not come through Memoh (the container's own init, a docker exec).
func TestWorkspaceImageDeclaresTheSameEnvironment(t *testing.T) {
	dockerfile, err := os.ReadFile("../../../docker/Dockerfile.workspace")
	if err != nil {
		t.Fatalf("read workspace Dockerfile: %v", err)
	}
	text := string(dockerfile)
	for _, entry := range Env() {
		if !strings.Contains(text, entry) {
			t.Fatalf("docker/Dockerfile.workspace does not declare %q", entry)
		}
	}
	if !strings.Contains(text, `test "$(getent passwd root | cut -d: -f6)" = `+Home) {
		t.Fatalf("docker/Dockerfile.workspace does not make %q the passwd home", Home)
	}
}

func TestWithLetsExtraEntriesWin(t *testing.T) {
	env := With("HOME=/elsewhere")
	if env[len(env)-1] != "HOME=/elsewhere" {
		t.Fatalf("With did not append the extra entry last: %q", env)
	}
}
