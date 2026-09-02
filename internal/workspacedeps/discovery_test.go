package workspacedeps

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

const discoveryToolAYAML = `id: tool-a
name: Tool A
category: tool
source: managed
provides: [tool-a, tool-a-helper]
platforms:
  - { os: linux, arch: [amd64, arm64], libc: glibc }
  - { os: darwin, arch: [arm64, amd64] }
scripts:
  install: install.sh
  remove: remove.sh
`

const discoveryToolBYAML = `id: tool-b
name: Tool B
category: agent
source: managed
provides: [tool-b]
platforms:
  - { os: linux, arch: [amd64, arm64], libc: glibc }
  - { os: darwin, arch: [arm64, amd64] }
version:
  pin: "7.7.7"
scripts:
  install: install.sh
  remove: remove.sh
  version: version.sh
`

// versionScript probes the candidate with a custom flag instead of
// --version, which is exactly what scripts.version exists for.
const versionScript = `dep_result "{\"version\":\"$("$MEMOH_DEP_CANDIDATE" --probe)\"}"
`

func discoveryCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	script := "dep_log noop\n"
	fsys := fstest.MapFS{
		"tool-a/dependency.yaml": &fstest.MapFile{Data: []byte(discoveryToolAYAML)},
		"tool-a/install.sh":      &fstest.MapFile{Data: []byte(script)},
		"tool-a/remove.sh":       &fstest.MapFile{Data: []byte(script)},
		"tool-b/dependency.yaml": &fstest.MapFile{Data: []byte(discoveryToolBYAML)},
		"tool-b/install.sh":      &fstest.MapFile{Data: []byte(script)},
		"tool-b/remove.sh":       &fstest.MapFile{Data: []byte(script)},
		"tool-b/version.sh":      &fstest.MapFile{Data: []byte(versionScript)},
	}
	cat, err := catalog.LoadFS(fsys)
	if err != nil {
		t.Fatalf("LoadFS: %v", err)
	}
	return cat
}

type discoveryFixture struct {
	*runFixture
	cat *catalog.Catalog
	// bin holds fake executables; tests put it on PATH when they want a PATH
	// copy to be found.
	bin string
}

func newDiscoveryFixture(t *testing.T) *discoveryFixture {
	t.Helper()
	f := &discoveryFixture{runFixture: newRunFixture(t), cat: discoveryCatalog(t), bin: t.TempDir()}
	writeExecutable(t, f.bin, "tool-a", "echo \"tool-a v 9.9.9 (build 42)\"\n")
	writeExecutable(t, f.bin, "tool-a-helper", "echo helper\n")
	writeExecutable(t, f.bin, "tool-b", "case \"$1\" in --probe) echo 7.7.7 ;; *) echo 'tool-b (no version flag)' ;; esac\n")
	return f
}

func (f *discoveryFixture) writeState(t *testing.T, depID string, state State) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	f.writeStateRaw(t, depID, data)
}

func (f *discoveryFixture) writeStateRaw(t *testing.T, depID string, data []byte) {
	t.Helper()
	home := Home(f.dataRoot, depID)
	if err := os.MkdirAll(home, 0o750); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(StatePath(home), data, 0o600); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
}

// withPath prepends dir to PATH for the rest of the test. bridgesvc inherits
// the test process environment, so `command -v` inside the exec sees it.
func withPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (f *discoveryFixture) discover(t *testing.T, ids ...string) map[string]Observed {
	t.Helper()
	observed, err := Discover(testContext(t), f.client, f.cat, f.dataRoot, ids, f.platform)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return observed
}

func TestDiscoverManagedCopyFromStateJSON(t *testing.T) {
	f := newDiscoveryFixture(t)
	entrypoint := filepath.Join(f.bin, "tool-a")
	f.writeState(t, "tool-a", State{
		DependencyID:   "tool-a",
		Version:        "9.9.9",
		InstalledAt:    time.Now().UTC().Truncate(time.Second),
		ManifestDigest: "sha256:abc",
		Entrypoints:    map[string]string{"tool-a": entrypoint, "tool-a-helper": filepath.Join(f.bin, "tool-a-helper")},
	})

	obs := f.discover(t, "tool-a")["tool-a"]
	if !obs.Present || obs.Source != SourceManaged {
		t.Fatalf("Observed = %+v, want a present managed copy", obs)
	}
	if obs.Command != entrypoint {
		t.Errorf("Command = %q, want %q", obs.Command, entrypoint)
	}
	if obs.Version != "9.9.9" {
		t.Errorf("Version = %q, want 9.9.9 from the --version probe", obs.Version)
	}
	if obs.State == nil || obs.State.ManifestDigest != "sha256:abc" {
		t.Errorf("State = %+v, want the decoded state.json", obs.State)
	}
	if got := obs.Entrypoints["tool-a-helper"]; got != filepath.Join(f.bin, "tool-a-helper") {
		t.Errorf("Entrypoints = %v, want state.json entrypoints", obs.Entrypoints)
	}
	if len(obs.Candidates) != 1 || obs.Candidates[0].Source != SourceManaged || obs.Candidates[0].Version != "9.9.9" {
		t.Errorf("Candidates = %+v, want one managed candidate at 9.9.9", obs.Candidates)
	}
	if obs.Err != "" {
		t.Errorf("Err = %q, want empty", obs.Err)
	}
}

func TestDiscoverManagedBeatsPathCopyAndDeduplicatesByPath(t *testing.T) {
	f := newDiscoveryFixture(t)
	withPath(t, f.bin)
	entrypoint := filepath.Join(f.bin, "tool-a")
	f.writeState(t, "tool-a", State{DependencyID: "tool-a", Version: "9.9.9", Entrypoints: map[string]string{"tool-a": entrypoint}})

	obs := f.discover(t, "tool-a")["tool-a"]
	if obs.Source != SourceManaged {
		t.Fatalf("Source = %q, want managed", obs.Source)
	}
	// PATH resolves to the very same file as the managed entrypoint; it must
	// not show up as a second candidate.
	if len(obs.Candidates) != 1 {
		t.Errorf("Candidates = %+v, want the duplicate PATH copy collapsed", obs.Candidates)
	}
}

func TestDiscoverPathCopyWithoutStateOrToolkit(t *testing.T) {
	f := newDiscoveryFixture(t)
	withPath(t, f.bin)

	obs := f.discover(t, "tool-a")["tool-a"]
	if !obs.Present || obs.Source != SourcePath {
		t.Fatalf("Observed = %+v, want a PATH copy", obs)
	}
	if obs.Command != filepath.Join(f.bin, "tool-a") {
		t.Errorf("Command = %q, want the fake binary", obs.Command)
	}
	if obs.Version != "9.9.9" {
		t.Errorf("Version = %q, want 9.9.9", obs.Version)
	}
	if obs.State != nil {
		t.Errorf("State = %+v, want nil without state.json", obs.State)
	}
	want := map[string]string{"tool-a": filepath.Join(f.bin, "tool-a"), "tool-a-helper": filepath.Join(f.bin, "tool-a-helper")}
	for name, path := range want {
		if obs.Entrypoints[name] != path {
			t.Errorf("Entrypoints[%s] = %q, want %q", name, obs.Entrypoints[name], path)
		}
	}
}

func TestDiscoverAbsentDependency(t *testing.T) {
	f := newDiscoveryFixture(t)
	// Do not put f.bin on PATH: nothing provides tool-a.
	obs := f.discover(t, "tool-a")["tool-a"]
	if obs.Present || obs.Source != "" || obs.Command != "" || len(obs.Candidates) != 0 {
		t.Errorf("Observed = %+v, want absent", obs)
	}
}

func TestDiscoverCorruptStateFallsBackToOtherSources(t *testing.T) {
	f := newDiscoveryFixture(t)
	withPath(t, f.bin)
	f.writeStateRaw(t, "tool-a", []byte("{not json"))

	obs := f.discover(t, "tool-a")["tool-a"]
	if !strings.Contains(obs.Err, "state.json") {
		t.Errorf("Err = %q, want a state.json parse problem", obs.Err)
	}
	if !obs.Present || obs.Source != SourcePath {
		t.Errorf("Observed = %+v, want the PATH copy to win when state.json is corrupt", obs)
	}
	if obs.State != nil {
		t.Errorf("State = %+v, want nil for corrupt state.json", obs.State)
	}
}

func TestDiscoverStateWithMissingEntrypointIsNotManaged(t *testing.T) {
	f := newDiscoveryFixture(t)
	withPath(t, f.bin)
	f.writeState(t, "tool-a", State{DependencyID: "tool-a", Version: "1.0.0", Entrypoints: map[string]string{"tool-a": filepath.Join(f.dataRoot, "gone", "tool-a")}})

	obs := f.discover(t, "tool-a")["tool-a"]
	if obs.Source != SourcePath {
		t.Errorf("Source = %q, want path when the managed entrypoint is gone", obs.Source)
	}
	if !strings.Contains(obs.Err, "not executable") {
		t.Errorf("Err = %q, want a note about the unusable entrypoint", obs.Err)
	}
	if obs.State == nil {
		t.Error("State must still be returned so callers can see what was recorded")
	}
}

func TestDiscoverIgnoresOwnShims(t *testing.T) {
	f := newDiscoveryFixture(t)
	shimDir := ShimDir(f.dataRoot)
	if err := os.MkdirAll(shimDir, 0o750); err != nil {
		t.Fatalf("mkdir shims: %v", err)
	}
	writeExecutable(t, shimDir, "tool-a", "echo shim 1.0.0\n")
	withPath(t, shimDir)

	obs := f.discover(t, "tool-a")["tool-a"]
	if obs.Present {
		t.Errorf("Observed = %+v, want our own shim to be ignored", obs)
	}
}

func TestDiscoverUsesVersionScriptWhenConfigured(t *testing.T) {
	f := newDiscoveryFixture(t)
	withPath(t, f.bin)
	entrypoint := filepath.Join(f.bin, "tool-b")
	f.writeState(t, "tool-b", State{DependencyID: "tool-b", Version: "0.0.1", Entrypoints: map[string]string{"tool-b": entrypoint}})

	observed := f.discover(t, "tool-a", "tool-b")
	obs := observed["tool-b"]
	if obs.Source != SourceManaged {
		t.Fatalf("Source = %q, want managed", obs.Source)
	}
	if obs.Version != "7.7.7" {
		t.Errorf("Version = %q, want 7.7.7 from scripts.version (state.json said 0.0.1)", obs.Version)
	}
	if obs.Err != "" {
		t.Errorf("Err = %q, want empty", obs.Err)
	}
	// Both dependencies were covered by the same call.
	if a := observed["tool-a"]; !a.Present || a.Version != "9.9.9" {
		t.Errorf("tool-a = %+v, want it discovered alongside tool-b", a)
	}
	// The read-only version probe must not create a home for a dependency.
	if _, err := os.Stat(VersionsDir(Home(f.dataRoot, "tool-b"))); err == nil {
		t.Error("version probe created versions/ under the dependency home")
	}
}

func TestDiscoverRejectsUnknownDependency(t *testing.T) {
	f := newDiscoveryFixture(t)
	if _, err := Discover(testContext(t), f.client, f.cat, f.dataRoot, []string{"nope"}, f.platform); err == nil {
		t.Error("Discover accepted an unknown dependency id")
	}
}

func TestDiscoverEmptyListIsNoop(t *testing.T) {
	f := newDiscoveryFixture(t)
	observed := f.discover(t)
	if len(observed) != 0 {
		t.Errorf("Observed = %v, want empty", observed)
	}
}

func TestParseDiscoveryOutputToolkitPrecedence(t *testing.T) {
	dep := catalog.Dependency{ID: "codex", Provides: []string{"codex"}}
	stdout := strings.Join([]string{
		"__MEMOH_DEP__\tcodex",
		"__MEMOH_TOOLKIT__\tcodex\t/opt/memoh/toolkit/bin/codex",
		"__MEMOH_VERSION_BEGIN__\t/opt/memoh/toolkit/bin/codex",
		"codex-cli 0.150.0",
		"",
		"__MEMOH_VERSION_END__",
		"__MEMOH_PATH__\tcodex\t/usr/local/bin/codex",
		"__MEMOH_VERSION_BEGIN__\t/usr/local/bin/codex",
		"codex-cli 0.151.0-rc.1",
		"",
		"__MEMOH_VERSION_END__",
		"__MEMOH_END__",
		"",
	}, "\n")
	probes, complete := parseDiscoveryOutput(stdout)
	if !complete {
		t.Fatal("end marker not detected")
	}
	obs := resolveObserved(dep, probes["codex"])
	if obs.Source != SourceToolkit || obs.Command != "/opt/memoh/toolkit/bin/codex" || obs.Version != "0.150.0" {
		t.Errorf("Observed = %+v, want the toolkit copy at 0.150.0 to win", obs)
	}
	if len(obs.Candidates) != 2 || obs.Candidates[1].Source != SourcePath || obs.Candidates[1].Version != "0.151.0-rc.1" {
		t.Errorf("Candidates = %+v, want toolkit then PATH with a pre-release version", obs.Candidates)
	}

	// The toolkit bin being first on PATH is the usual case: same path, one
	// candidate.
	same := strings.ReplaceAll(stdout, "/usr/local/bin/codex", "/opt/memoh/toolkit/bin/codex")
	probes, _ = parseDiscoveryOutput(same)
	if obs := resolveObserved(dep, probes["codex"]); len(obs.Candidates) != 1 || obs.Source != SourceToolkit {
		t.Errorf("Observed = %+v, want one toolkit candidate", obs)
	}
}

func TestParseDiscoveryOutputIncompleteWithoutEndMarker(t *testing.T) {
	if _, complete := parseDiscoveryOutput("__MEMOH_DEP__\tx\n"); complete {
		t.Error("output without end marker reported complete")
	}
}

func TestExtractVersion(t *testing.T) {
	cases := map[string]string{
		"v22.1.0":                        "22.1.0",
		"Python 3.12.1":                  "3.12.1",
		"codex-cli 0.151.0":              "0.151.0",
		"2.1.0 (Claude Code)":            "2.1.0",
		"1.2.3-beta.1 extra 4.5.6":       "1.2.3-beta.1",
		"no version here":                "",
		"uv 0.4.18 (7b55e9790 2024-09)":  "0.4.18",
		"multi\nline\nnode v20.11.1\n":   "20.11.1",
		"short 1.2 then longer 10.20.30": "10.20.30",
	}
	for in, want := range cases {
		if got := extractVersion(in); got != want {
			t.Errorf("extractVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEntrypointSedPattern(t *testing.T) {
	if _, ok := entrypointSedPattern("has space"); ok {
		t.Error("pattern accepted a command name with a space")
	}
	pattern, ok := entrypointSedPattern("python3.12")
	if !ok || !strings.Contains(pattern, `python3\.12`) {
		t.Errorf("pattern = %q, ok = %v; want dots escaped", pattern, ok)
	}
}
