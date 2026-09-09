package workspacedeps

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

func TestRemovalScriptDeletesWorkspaceCopies(t *testing.T) {
	for _, tc := range []struct {
		id       string
		commands []string
		trees    []string
	}{
		{"node", []string{"node", "npm", "npx"}, []string{"node-glibc", "node-musl"}},
		{"python", []string{"python", "python3", "pip", "pip3"}, []string{"python-gnu", "python-musl", "python-gnu-x86_64", "python-gnu-aarch64", "python-musl-x86_64", "python-musl-aarch64"}},
		{"uv", []string{"uv", "uvx"}, []string{"uv"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			root := t.TempDir()
			toolkit := filepath.Join(root, "toolkit with spaces")
			home := filepath.Join(root, "managed", tc.id)
			writeRemovalFixture(t, filepath.Join(home, "state.json"))
			for _, command := range tc.commands {
				writeRemovalFixture(t, filepath.Join(toolkit, "bin", command))
			}
			for _, tree := range tc.trees {
				name := filepath.Join(toolkit, tree)
				if tc.id != "uv" {
					name = filepath.Join(name, "bin", "runtime")
				}
				writeRemovalFixture(t, name)
			}
			unrelated := filepath.Join(toolkit, "display", "bin", "a11y-cli")
			writeRemovalFixture(t, unrelated)
			dep := catalog.Dependency{ID: tc.id, Provides: tc.commands}
			// Recipes commonly exit explicitly; cleanup must still run.
			body := removalScript(dep, "exit 0", toolkit)
			if out, err := runRemovalFixture(t, body, home, TargetNative); err != nil {
				t.Fatalf("remove: %v: %s", err, out)
			}
			for _, name := range append(append([]string{home}, removalFixturePaths(toolkit, "bin", tc.commands)...), removalFixturePaths(toolkit, "", tc.trees)...) {
				if _, err := os.Lstat(name); !os.IsNotExist(err) {
					t.Errorf("dependency file remains: %s (%v)", name, err)
				}
			}
			if _, err := os.Stat(unrelated); err != nil {
				t.Fatalf("unrelated toolkit file removed: %v", err)
			}
			// Repeated removal is safe, including when no managed copy existed.
			if out, err := runRemovalFixture(t, body, home, TargetNative); err != nil {
				t.Fatalf("repeat remove: %v: %s", err, out)
			}
		})
	}
}

func TestRemovalScriptFailureAndRemoteScope(t *testing.T) {
	for _, tc := range []struct {
		name, target, script string
		failed               bool
	}{
		{"failed recipe", TargetNative, "exit 42", true},
		{"remote target", "remote-test", "exit 0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "managed")
			toolkit := filepath.Join(root, "toolkit")
			state := filepath.Join(home, "state.json")
			command := filepath.Join(toolkit, "bin", "uv")
			writeRemovalFixture(t, state)
			writeRemovalFixture(t, command)
			body := removalScript(catalog.Dependency{ID: "uv", Provides: []string{"uv"}}, tc.script, toolkit)
			if out, err := runRemovalFixture(t, body, home, tc.target); (err != nil) != tc.failed {
				t.Fatalf("remove: %v: %s", err, out)
			}
			if _, err := os.Stat(command); err != nil {
				t.Fatalf("toolkit must remain untouched: %v", err)
			}
			if tc.failed {
				if _, err := os.Stat(state); err != nil {
					t.Fatalf("cleanup ran after a failed recipe: %v", err)
				}
			}
		})
	}
}

func TestRemovalScriptDoesNotFollowToolkitSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "unrelated")
	writeRemovalFixture(t, filepath.Join(outside, "keep"))
	toolkit := filepath.Join(root, "toolkit")
	if err := os.Symlink(outside, toolkit); err != nil {
		t.Fatal(err)
	}
	body := removalScript(catalog.Dependency{ID: "uv", Provides: []string{"../keep", "uv"}}, ":", toolkit)
	if out, err := runRemovalFixture(t, body, filepath.Join(root, "managed"), TargetNative); err == nil {
		t.Fatalf("symlink ancestor must fail: %s", out)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("followed toolkit symlink")
	}
}

func writeRemovalFixture(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runRemovalFixture(t *testing.T, body, home, target string) ([]byte, error) {
	cmd := exec.CommandContext(t.Context(), "sh", "-s")
	cmd.Stdin = strings.NewReader("set -eu\n" + body)
	cmd.Env = append(os.Environ(), "MEMOH_DEP_HOME="+home, "MEMOH_DEP_WORKSPACE_TARGET="+target)
	return cmd.CombinedOutput()
}

func removalFixturePaths(root, dir string, names []string) []string {
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, filepath.Join(root, dir, name))
	}
	return paths
}
