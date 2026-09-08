package catalog

import (
	"strings"
	"testing"
	"testing/fstest"
)

// validImageYAML is an image-baseline dependency without scripts: visible in
// the catalog, not installable.
const validImageYAML = `id: node
name: Node.js
category: runtime
source: image
provides: [node, npm]
platforms:
  - { os: linux, arch: [amd64, arm64], libc: glibc }
`

// validOverlayYAML is an image-baseline dependency whose scripts install
// overlay versions on top of the image copy.
const validOverlayYAML = validImageYAML + `scripts:
  install: install.sh
  update: update.sh
  remove: remove.sh
  check_update: check-update.sh
`

const validManagedYAML = `id: tool-a
name: Tool A
category: tool
source: managed
requires: [node]
provides: [tool-a]
platforms:
  - { os: linux, arch: [amd64, arm64], libc: glibc }
scripts:
  install: install.sh
  remove: remove.sh
`

// validAgentYAML is an unpinned agent: latest by default, upstream checks
// allowed, exactly like any other managed dependency.
const validAgentYAML = `id: agent-a
name: Agent A
category: agent
source: managed
requires: [node]
provides: [agent-a]
platforms:
  - { os: linux, arch: [amd64], libc: glibc }
scripts:
  install: install.sh
  update: update.sh
  remove: remove.sh
  check_update: check-update.sh
`

const scriptBody = "dep_log installing\ndep_result '{}'\n"

// allScriptNames are the script files the fixtures may reference.
var allScriptNames = []string{"install.sh", "update.sh", "remove.sh", "check-update.sh"}

// testFS builds a catalog filesystem from dir → file name → content.
func testFS(dirs map[string]map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for dir, files := range dirs {
		for name, content := range files {
			fsys[dir+"/"+name] = &fstest.MapFile{Data: []byte(content)}
		}
	}
	return fsys
}

// withScripts returns the manifest next to every script file in
// allScriptNames; unreferenced files do not affect validation or digests.
func withScripts(manifest string) map[string]string {
	files := map[string]string{ManifestFileName: manifest}
	for _, name := range allScriptNames {
		files[name] = scriptBody
	}
	return files
}

func imageFiles(manifest string) map[string]string {
	return map[string]string{ManifestFileName: manifest}
}

func TestLoadFSAcceptsValidCatalog(t *testing.T) {
	fsys := testFS(map[string]map[string]string{
		"node":    withScripts(validOverlayYAML),
		"tool-a":  withScripts(validManagedYAML),
		"agent-a": withScripts(validAgentYAML),
	})
	fsys["README.md"] = &fstest.MapFile{Data: []byte("root files are ignored")}
	c, err := LoadFS(fsys)
	if err != nil {
		t.Fatalf("LoadFS() error = %v", err)
	}
	if got := len(c.List()); got != 3 {
		t.Fatalf("List() len = %d, want 3", got)
	}
	tool := c.MustGet("tool-a")
	if tool.Timeouts != (Timeouts{Install: 1200, Update: 1200, Remove: 300, CheckUpdate: 60, Version: 30}) {
		t.Fatalf("default timeouts = %+v", tool.Timeouts)
	}
	if tool.Timeouts.For(ActionReinstall) != 1500 {
		t.Fatalf("reinstall timeout = %d", tool.Timeouts.For(ActionReinstall))
	}
	if script, ok := c.Script("tool-a", ActionInstall); !ok || script != scriptBody {
		t.Fatalf("Script(tool-a, install) = %q, %v", script, ok)
	}
	if _, ok := c.Script("tool-a", ActionUpdate); ok {
		t.Fatal("update should not be configured for tool-a")
	}
	node := c.MustGet("node")
	if !node.HasImageBaseline() || !node.Installable() {
		t.Fatalf("node overlay entry = %+v", node)
	}
	if script, ok := c.Script("node", ActionCheckUpdate); !ok || script != scriptBody {
		t.Fatalf("Script(node, check_update) = %q, %v", script, ok)
	}
	agent := c.MustGet("agent-a")
	if !agent.IsAgent() || agent.Version.Pin != "" || agent.Scripts.CheckUpdate == "" {
		t.Fatalf("unpinned agent = %+v", agent)
	}
	if c.Validate() != nil {
		t.Fatal("Validate() on a loaded catalog should pass")
	}
}

func TestImageBaselineWithoutScriptsIsNotInstallable(t *testing.T) {
	c, err := LoadFS(testFS(map[string]map[string]string{"node": imageFiles(validImageYAML)}))
	if err != nil {
		t.Fatalf("LoadFS() error = %v", err)
	}
	node := c.MustGet("node")
	if !node.HasImageBaseline() || !node.IsImageProvided() {
		t.Fatalf("node should have an image baseline: %+v", node)
	}
	if node.Installable() {
		t.Fatal("an image entry without scripts must not be installable")
	}
	for _, action := range scriptActions {
		if _, ok := c.Script("node", action); ok {
			t.Errorf("Script(node, %s) should not be configured", action)
		}
	}
}

func TestTimeoutDefaultsFollowInstall(t *testing.T) {
	fsys := testFS(map[string]map[string]string{
		"node": imageFiles(validImageYAML),
		"tool-a": withScripts(strings.Replace(validManagedYAML, "scripts:\n",
			"timeouts:\n  install: 100\nscripts:\n", 1)),
	})
	c, err := LoadFS(fsys)
	if err != nil {
		t.Fatalf("LoadFS() error = %v", err)
	}
	got := c.MustGet("tool-a").Timeouts
	if got.Install != 100 || got.Update != 100 || got.Remove != 300 || got.CheckUpdate != 60 || got.Version != 30 {
		t.Fatalf("timeouts = %+v", got)
	}
	if got.Duration(ActionVersion).Seconds() != 30 {
		t.Fatalf("Duration(version) = %s", got.Duration(ActionVersion))
	}
}

// TestValidateAcceptsVersionAndSourceCombinations covers the rules that were
// deliberately relaxed: pin is optional everywhere, agents may check upstream,
// and image-baseline entries may carry overlay scripts.
func TestValidateAcceptsVersionAndSourceCombinations(t *testing.T) {
	replace := func(base, old, repl string) string {
		if !strings.Contains(base, old) {
			t.Fatalf("test fixture: %q not found in manifest", old)
		}
		return strings.Replace(base, old, repl, 1)
	}
	tests := []struct {
		name  string
		files map[string]string
		check func(t *testing.T, dep Dependency)
	}{
		{
			name:  "agent without pin",
			files: withScripts(validAgentYAML),
			check: func(t *testing.T, dep Dependency) {
				if dep.Version.Pin != "" || dep.Scripts.CheckUpdate == "" {
					t.Fatalf("dep = %+v", dep)
				}
			},
		},
		{
			name:  "agent with pin",
			files: withScripts(validAgentYAML + "version:\n  pin: \"1.2.3\"\n"),
			check: func(t *testing.T, dep Dependency) {
				if dep.Version.Pin != "1.2.3" {
					t.Fatalf("pin = %q", dep.Version.Pin)
				}
			},
		},
		{
			name:  "agent with pin and check_update",
			files: withScripts(validAgentYAML + "version:\n  pin: \"1.2.3\"\n  channel: stable\n"),
			check: func(t *testing.T, dep Dependency) {
				if dep.Version.Pin != "1.2.3" || dep.Version.Channel != "stable" || dep.Scripts.CheckUpdate == "" {
					t.Fatalf("dep = %+v", dep)
				}
			},
		},
		{
			name:  "tool with pin",
			files: withScripts(replace(validManagedYAML, "id: tool-a", "id: agent-a") + "version:\n  pin: \"9.9.9\"\n"),
			check: func(t *testing.T, dep Dependency) {
				if dep.Version.Pin != "9.9.9" {
					t.Fatalf("pin = %q", dep.Version.Pin)
				}
			},
		},
		{
			name:  "image entry with overlay scripts",
			files: withScripts(replace(validOverlayYAML, "id: node", "id: agent-a")),
			check: func(t *testing.T, dep Dependency) {
				if !dep.HasImageBaseline() || !dep.Installable() || dep.Scripts.CheckUpdate == "" {
					t.Fatalf("dep = %+v", dep)
				}
			},
		},
		{
			name:  "image entry with install and remove only",
			files: withScripts(replace(validImageYAML, "id: node", "id: agent-a") + "scripts:\n  install: install.sh\n  remove: remove.sh\n"),
			check: func(t *testing.T, dep Dependency) {
				if !dep.Installable() || dep.Scripts.Update != "" {
					t.Fatalf("dep = %+v", dep)
				}
			},
		},
		{
			name:  "image entry with pin",
			files: withScripts(replace(validOverlayYAML, "id: node", "id: agent-a") + "version:\n  pin: \"22.12.0\"\n"),
			check: func(t *testing.T, dep Dependency) {
				if dep.Version.Pin != "22.12.0" || !dep.HasImageBaseline() {
					t.Fatalf("dep = %+v", dep)
				}
			},
		},
		{
			name:  "image entry without scripts",
			files: imageFiles(replace(validImageYAML, "id: node", "id: agent-a")),
			check: func(t *testing.T, dep Dependency) {
				if dep.Installable() {
					t.Fatalf("dep = %+v", dep)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Every fixture is stored under agent-a so requires: [node] keeps
			// resolving against the plain image entry.
			fsys := testFS(map[string]map[string]string{"node": imageFiles(validImageYAML), "agent-a": tt.files})
			c, err := LoadFS(fsys)
			if err != nil {
				t.Fatalf("LoadFS() error = %v", err)
			}
			tt.check(t, c.MustGet("agent-a"))
		})
	}
}

func TestValidateRejectsInvalidManifests(t *testing.T) {
	replace := func(base, old, repl string) string {
		if !strings.Contains(base, old) {
			t.Fatalf("test fixture: %q not found in manifest", old)
		}
		return strings.Replace(base, old, repl, 1)
	}
	withNode := func(dir string, files map[string]string) fstest.MapFS {
		return testFS(map[string]map[string]string{"node": imageFiles(validImageYAML), dir: files})
	}

	tests := []struct {
		name string
		fsys fstest.MapFS
		want string
	}{
		{
			name: "empty id",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "id: tool-a", `id: ""`))),
			want: `id must not be empty (directory "tool-a")`,
		},
		{
			name: "invalid id characters",
			fsys: withNode("Tool_A", withScripts(replace(validManagedYAML, "id: tool-a", "id: Tool_A"))),
			want: `id "Tool_A" must only contain [a-z0-9-]`,
		},
		{
			name: "id differs from directory",
			fsys: withNode("tool-b", withScripts(validManagedYAML)),
			want: `id "tool-a" does not match directory name "tool-b"`,
		},
		{
			name: "duplicate id",
			fsys: testFS(map[string]map[string]string{
				"node":   imageFiles(validImageYAML),
				"tool-a": withScripts(validManagedYAML),
				"tool-b": withScripts(validManagedYAML),
			}),
			want: `id "tool-a" is declared by more than one directory`,
		},
		{
			name: "missing name",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "name: Tool A\n", ""))),
			want: `dependency "tool-a": name must not be empty`,
		},
		{
			name: "unknown category",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "category: tool", "category: plugin"))),
			want: `category "plugin" must be one of agent, runtime, tool`,
		},
		{
			name: "unknown source",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "source: managed", "source: remote"))),
			want: `source "remote" must be one of managed, image`,
		},
		{
			name: "empty provides",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "provides: [tool-a]", "provides: []"))),
			want: "provides must list at least one command",
		},
		{
			name: "empty platforms",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML,
				"platforms:\n  - { os: linux, arch: [amd64, arm64], libc: glibc }\n", "platforms: []\n"))),
			want: "platforms must list at least one platform",
		},
		{
			name: "platform without os",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML,
				"{ os: linux, arch: [amd64, arm64], libc: glibc }", "{ arch: [amd64] }"))),
			want: "platforms[0]: os must not be empty",
		},
		{
			name: "platform without arch",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML,
				"{ os: linux, arch: [amd64, arm64], libc: glibc }", "{ os: linux }"))),
			want: "platforms[0]: arch must list at least one architecture",
		},
		{
			name: "requires unknown dependency",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "requires: [node]", "requires: [ghost]"))),
			want: `requires unknown dependency "ghost"`,
		},
		{
			name: "requires itself",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "requires: [node]", "requires: [tool-a]"))),
			want: "requires must not reference itself",
		},
		{
			name: "image source for agent",
			fsys: testFS(map[string]map[string]string{"node": imageFiles(replace(validImageYAML,
				"category: runtime", "category: agent"))}),
			want: "source image cannot be used with category agent",
		},
		{
			name: "managed without install script",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "  install: install.sh\n", ""))),
			want: "source managed requires scripts.install",
		},
		{
			name: "managed without remove script",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "  remove: remove.sh\n", ""))),
			want: "scripts.remove is required when scripts.install is set",
		},
		{
			name: "image install without remove script",
			fsys: testFS(map[string]map[string]string{"node": withScripts(validImageYAML + "scripts:\n  install: install.sh\n")}),
			want: "scripts.remove is required when scripts.install is set",
		},
		{
			name: "image update without install script",
			fsys: testFS(map[string]map[string]string{"node": withScripts(validImageYAML + "scripts:\n  update: update.sh\n")}),
			want: "scripts.update requires scripts.install",
		},
		{
			name: "image check_update without install script",
			fsys: testFS(map[string]map[string]string{"node": withScripts(validImageYAML + "scripts:\n  check_update: check-update.sh\n")}),
			want: "scripts.check_update requires scripts.install",
		},
		{
			name: "image version without install script",
			fsys: testFS(map[string]map[string]string{"node": withScripts(validImageYAML + "scripts:\n  version: install.sh\n")}),
			want: "scripts.version requires scripts.install",
		},
		{
			name: "image remove without install script",
			fsys: testFS(map[string]map[string]string{"node": withScripts(validImageYAML + "scripts:\n  remove: remove.sh\n")}),
			want: "",
		},
		{
			name: "script file missing",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "install: install.sh", "install: missing.sh"))),
			want: `scripts.install: file "missing.sh" does not exist`,
		},
		{
			name: "script file empty",
			fsys: withNode("tool-a", func() map[string]string {
				files := withScripts(validManagedYAML)
				files["remove.sh"] = " \n"
				return files
			}()),
			want: `scripts.remove: file "remove.sh" is empty`,
		},
		{
			name: "script path escapes directory",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "install: install.sh", "install: ../install.sh"))),
			want: `scripts.install: "../install.sh" must be a plain file name`,
		},
		{
			name: "negative timeout",
			fsys: withNode("tool-a", withScripts(replace(validManagedYAML, "scripts:\n", "timeouts:\n  remove: -5\nscripts:\n"))),
			want: "timeouts.remove must be positive",
		},
		{
			name: "unknown manifest field",
			fsys: withNode("tool-a", withScripts(validManagedYAML+"flavour: spicy\n")),
			want: "field flavour not found",
		},
		{
			name: "missing manifest",
			fsys: withNode("tool-a", map[string]string{"install.sh": scriptBody}),
			want: "tool-a: read dependency.yaml",
		},
		{
			name: "empty manifest",
			fsys: withNode("tool-a", withScripts("")),
			want: "manifest is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := LoadFS(tt.fsys)
			if tt.want == "" {
				// A remove-only image entry is odd but harmless: there is nothing
				// to install, so the rule set has nothing to say about it.
				if err != nil {
					t.Fatalf("LoadFS() error = %v, want success", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("LoadFS() succeeded with %d dependencies, want error containing %q", len(c.List()), tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadFS() error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestSupportsPlatform(t *testing.T) {
	dep := Dependency{Platforms: []Platform{
		{OS: "linux", Arch: []string{"amd64", "arm64"}, Libc: "glibc"},
		{OS: "darwin", Arch: []string{"arm64"}},
	}}
	tests := []struct {
		name           string
		os, arch, libc string
		want           bool
	}{
		{name: "linux glibc matches", os: "linux", arch: "amd64", libc: "glibc", want: true},
		{name: "linux arm64 glibc matches", os: "linux", arch: "arm64", libc: "glibc", want: true},
		{name: "linux musl rejected", os: "linux", arch: "amd64", libc: "musl", want: false},
		{name: "linux unknown libc rejected", os: "linux", arch: "amd64", libc: "", want: false},
		{name: "darwin ignores libc", os: "darwin", arch: "arm64", libc: "", want: true},
		{name: "darwin ignores libc value", os: "darwin", arch: "arm64", libc: "musl", want: true},
		{name: "darwin amd64 rejected", os: "darwin", arch: "amd64", libc: "", want: false},
		{name: "unknown os rejected", os: "freebsd", arch: "amd64", libc: "glibc", want: false},
		{name: "case insensitive", os: "Linux", arch: "AMD64", libc: "GLIBC", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dep.SupportsPlatform(tt.os, tt.arch, tt.libc); got != tt.want {
				t.Fatalf("SupportsPlatform(%q, %q, %q) = %v, want %v", tt.os, tt.arch, tt.libc, got, tt.want)
			}
		})
	}
}

func TestDigestFiles(t *testing.T) {
	base := map[string][]byte{"dependency.yaml": []byte("id: a\n"), "install.sh": []byte("echo hi\n")}
	if got, again := DigestFiles(base), DigestFiles(base); got != again {
		t.Fatalf("digest not deterministic: %s vs %s", got, again)
	}
	changed := map[string][]byte{"dependency.yaml": []byte("id: a\n"), "install.sh": []byte("echo bye\n")}
	if DigestFiles(base) == DigestFiles(changed) {
		t.Fatal("digest should change with script content")
	}
	renamed := map[string][]byte{"dependency.yaml": []byte("id: a\n"), "setup.sh": []byte("echo hi\n")}
	if DigestFiles(base) == DigestFiles(renamed) {
		t.Fatal("digest should change when a script is renamed")
	}
	moved := map[string][]byte{"dependency.yaml": []byte("id: a\necho hi\n"), "install.sh": []byte("")}
	if DigestFiles(base) == DigestFiles(moved) {
		t.Fatal("digest should change when bytes move between files")
	}
}

func TestDigestIgnoresUnreferencedFiles(t *testing.T) {
	files := withScripts(validManagedYAML)
	plain := testFS(map[string]map[string]string{"node": imageFiles(validImageYAML), "tool-a": files})
	extra := testFS(map[string]map[string]string{"node": imageFiles(validImageYAML), "tool-a": files})
	extra["tool-a/NOTES.md"] = &fstest.MapFile{Data: []byte("unreferenced")}
	first, err := LoadFS(plain)
	if err != nil {
		t.Fatalf("LoadFS() error = %v", err)
	}
	second, err := LoadFS(extra)
	if err != nil {
		t.Fatalf("LoadFS() error = %v", err)
	}
	if first.MustGet("tool-a").ManifestDigest != second.MustGet("tool-a").ManifestDigest {
		t.Fatal("unreferenced files must not affect the manifest digest")
	}
	files["install.sh"] = scriptBody + "echo more\n"
	third, err := LoadFS(testFS(map[string]map[string]string{"node": imageFiles(validImageYAML), "tool-a": files}))
	if err != nil {
		t.Fatalf("LoadFS() error = %v", err)
	}
	if first.MustGet("tool-a").ManifestDigest == third.MustGet("tool-a").ManifestDigest {
		t.Fatal("referenced script changes must affect the manifest digest")
	}
}
