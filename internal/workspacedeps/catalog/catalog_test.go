package catalog

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

const validImageYAML = `id: node
name: Node.js
category: runtime
source: image
provides: [node, npm]
platforms:
  - { os: linux, arch: [amd64, arm64], libc: glibc }
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

const validAgentYAML = `id: agent-a
name: Agent A
category: agent
source: managed
requires: [node]
provides: [agent-a]
platforms:
  - { os: linux, arch: [amd64], libc: glibc }
version:
  pin: "1.2.3"
scripts:
  install: install.sh
  update: update.sh
  remove: remove.sh
`

const scriptBody = "dep_log installing\ndep_result '{}'\n"

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

func managedFiles(manifest string) map[string]string {
	return map[string]string{
		ManifestFileName: manifest,
		"install.sh":     scriptBody,
		"update.sh":      scriptBody,
		"remove.sh":      scriptBody,
	}
}

func imageFiles(manifest string) map[string]string {
	return map[string]string{ManifestFileName: manifest}
}

func TestLoadEmbeddedCatalog(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	var ids []string
	for _, dep := range c.List() {
		ids = append(ids, dep.ID)
	}
	want := []string{"claude-code", "codex", "node", "python", "uv"}
	if !slices.Equal(ids, want) {
		t.Fatalf("List() ids = %v, want %v", ids, want)
	}

	codex := c.MustGet("codex")
	if !codex.IsAgent() || codex.IsImageProvided() {
		t.Fatalf("codex category/source = %q/%q", codex.Category, codex.Source)
	}
	if !slices.Equal(codex.Requires, []string{"node"}) || !slices.Equal(codex.Provides, []string{"codex"}) {
		t.Fatalf("codex requires/provides = %v/%v", codex.Requires, codex.Provides)
	}
	if codex.Icon != "openai" || codex.Version.Pin == "" {
		t.Fatalf("codex icon/pin = %q/%q", codex.Icon, codex.Version.Pin)
	}
	if codex.Timeouts.Install != 1200 || codex.Timeouts.Update != 1200 || codex.Timeouts.Remove != 300 {
		t.Fatalf("codex timeouts = %+v", codex.Timeouts)
	}
	if !codex.SupportsPlatform("linux", "amd64", "glibc") || !codex.SupportsPlatform("darwin", "arm64", "") {
		t.Fatalf("codex platforms = %+v", codex.Platforms)
	}

	claude := c.MustGet("claude-code")
	if !claude.IsAgent() || claude.Icon != "anthropic" || !slices.Equal(claude.Provides, []string{"claude"}) {
		t.Fatalf("claude-code = %+v", claude)
	}

	node := c.MustGet("node")
	if !node.IsImageProvided() || node.IsAgent() || !slices.Equal(node.Provides, []string{"node", "npm", "npx"}) {
		t.Fatalf("node = %+v", node)
	}
	if python := c.MustGet("python"); !slices.Equal(python.Provides, []string{"python3", "pip3"}) {
		t.Fatalf("python provides = %v", python.Provides)
	}
	if uv := c.MustGet("uv"); !slices.Equal(uv.Provides, []string{"uv", "uvx"}) {
		t.Fatalf("uv provides = %v", uv.Provides)
	}

	if _, ok := c.Get("ghost"); ok {
		t.Fatal("Get(ghost) should report missing")
	}
}

func TestEmbeddedScripts(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, id := range []string{"codex", "claude-code"} {
		for _, action := range []Action{ActionInstall, ActionUpdate} {
			script, ok := c.Script(id, action)
			if !ok {
				t.Fatalf("Script(%s, %s) missing", id, action)
			}
			for _, needle := range []string{"npm install -g", "dep_switch", "dep_result", "$MEMOH_DEP_HOME/versions/$MEMOH_DEP_VERSION"} {
				if !strings.Contains(script, needle) {
					t.Errorf("Script(%s, %s) lacks %q", id, action, needle)
				}
			}
			if strings.Contains(script, "/data") {
				t.Errorf("Script(%s, %s) hard-codes /data (WD-EXEC-001)", id, action)
			}
			for _, fn := range []string{"dep_log()", "dep_result()", "dep_switch()"} {
				if strings.Contains(script, fn) {
					t.Errorf("Script(%s, %s) redefines prelude function %s", id, action, fn)
				}
			}
		}
		remove, ok := c.Script(id, ActionRemove)
		if !ok || !strings.Contains(remove, `rm -rf "$MEMOH_DEP_HOME"`) || !strings.Contains(remove, "dep_result '{}'") {
			t.Errorf("Script(%s, remove) = %q, ok=%v", id, remove, ok)
		}
		for _, action := range []Action{ActionCheckUpdate, ActionReinstall, ActionVersion} {
			if _, ok := c.Script(id, action); ok {
				t.Errorf("Script(%s, %s) should not be configured", id, action)
			}
		}
	}
	for _, id := range []string{"node", "python", "uv"} {
		if _, ok := c.Script(id, ActionInstall); ok {
			t.Errorf("Script(%s, install) should not exist for image dependencies", id)
		}
	}
	if _, ok := c.Script("ghost", ActionInstall); ok {
		t.Error("Script(ghost) should report missing")
	}
}

func TestEmbeddedDigestsAreStableAndDistinct(t *testing.T) {
	first, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	second, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	seen := map[string]string{}
	for _, dep := range first.List() {
		if !strings.HasPrefix(dep.ManifestDigest, "sha256:") || len(dep.ManifestDigest) != len("sha256:")+64 {
			t.Fatalf("%s digest = %q", dep.ID, dep.ManifestDigest)
		}
		if other := second.MustGet(dep.ID); other.ManifestDigest != dep.ManifestDigest {
			t.Fatalf("%s digest changed between loads: %q vs %q", dep.ID, dep.ManifestDigest, other.ManifestDigest)
		}
		if prev, dup := seen[dep.ManifestDigest]; dup {
			t.Fatalf("%s and %s share digest %s", prev, dep.ID, dep.ManifestDigest)
		}
		seen[dep.ManifestDigest] = dep.ID
	}
}

func TestLoadFSAcceptsValidCatalog(t *testing.T) {
	fsys := testFS(map[string]map[string]string{
		"node":    imageFiles(validImageYAML),
		"tool-a":  managedFiles(validManagedYAML),
		"agent-a": managedFiles(validAgentYAML),
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
	if c.Validate() != nil {
		t.Fatal("Validate() on a loaded catalog should pass")
	}
}

func TestTimeoutDefaultsFollowInstall(t *testing.T) {
	fsys := testFS(map[string]map[string]string{
		"node": imageFiles(validImageYAML),
		"tool-a": managedFiles(strings.Replace(validManagedYAML, "scripts:\n",
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
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "id: tool-a", `id: ""`))),
			want: `id must not be empty (directory "tool-a")`,
		},
		{
			name: "invalid id characters",
			fsys: withNode("Tool_A", managedFiles(replace(validManagedYAML, "id: tool-a", "id: Tool_A"))),
			want: `id "Tool_A" must only contain [a-z0-9-]`,
		},
		{
			name: "id differs from directory",
			fsys: withNode("tool-b", managedFiles(validManagedYAML)),
			want: `id "tool-a" does not match directory name "tool-b"`,
		},
		{
			name: "duplicate id",
			fsys: testFS(map[string]map[string]string{
				"node":   imageFiles(validImageYAML),
				"tool-a": managedFiles(validManagedYAML),
				"tool-b": managedFiles(validManagedYAML),
			}),
			want: `id "tool-a" is declared by more than one directory`,
		},
		{
			name: "missing name",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "name: Tool A\n", ""))),
			want: `dependency "tool-a": name must not be empty`,
		},
		{
			name: "unknown category",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "category: tool", "category: plugin"))),
			want: `category "plugin" must be one of agent, runtime, tool`,
		},
		{
			name: "unknown source",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "source: managed", "source: remote"))),
			want: `source "remote" must be one of managed, image`,
		},
		{
			name: "empty provides",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "provides: [tool-a]", "provides: []"))),
			want: "provides must list at least one command",
		},
		{
			name: "empty platforms",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML,
				"platforms:\n  - { os: linux, arch: [amd64, arm64], libc: glibc }\n", "platforms: []\n"))),
			want: "platforms must list at least one platform",
		},
		{
			name: "platform without os",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML,
				"{ os: linux, arch: [amd64, arm64], libc: glibc }", "{ arch: [amd64] }"))),
			want: "platforms[0]: os must not be empty",
		},
		{
			name: "platform without arch",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML,
				"{ os: linux, arch: [amd64, arm64], libc: glibc }", "{ os: linux }"))),
			want: "platforms[0]: arch must list at least one architecture",
		},
		{
			name: "requires unknown dependency",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "requires: [node]", "requires: [ghost]"))),
			want: `requires unknown dependency "ghost"`,
		},
		{
			name: "requires itself",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "requires: [node]", "requires: [tool-a]"))),
			want: "requires must not reference itself",
		},
		{
			name: "image source with scripts",
			fsys: testFS(map[string]map[string]string{"node": {
				ManifestFileName: validImageYAML + "scripts:\n  install: install.sh\n",
				"install.sh":     scriptBody,
			}}),
			want: "source image must not declare scripts (WD-CAT-001)",
		},
		{
			name: "image source for agent",
			fsys: testFS(map[string]map[string]string{"node": imageFiles(replace(validImageYAML,
				"category: runtime", "category: agent\nversion:\n  pin: \"1.0.0\""))}),
			want: "source image cannot be used with category agent",
		},
		{
			name: "agent without pin",
			fsys: withNode("agent-a", managedFiles(replace(validAgentYAML, "version:\n  pin: \"1.2.3\"\n", ""))),
			want: "category agent requires version.pin (WD-CAT-004)",
		},
		{
			name: "agent with check_update script",
			fsys: withNode("agent-a", func() map[string]string {
				files := managedFiles(validAgentYAML + "  check_update: check.sh\n")
				files["check.sh"] = scriptBody
				return files
			}()),
			want: "category agent must not declare scripts.check_update (WD-CAT-004)",
		},
		{
			name: "managed without install script",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "  install: install.sh\n", ""))),
			want: "source managed requires scripts.install",
		},
		{
			name: "managed without remove script",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "  remove: remove.sh\n", ""))),
			want: "source managed requires scripts.remove",
		},
		{
			name: "script file missing",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "install: install.sh", "install: missing.sh"))),
			want: `scripts.install: file "missing.sh" does not exist`,
		},
		{
			name: "script file empty",
			fsys: withNode("tool-a", func() map[string]string {
				files := managedFiles(validManagedYAML)
				files["remove.sh"] = " \n"
				return files
			}()),
			want: `scripts.remove: file "remove.sh" is empty`,
		},
		{
			name: "script path escapes directory",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "install: install.sh", "install: ../install.sh"))),
			want: `scripts.install: "../install.sh" must be a plain file name`,
		},
		{
			name: "negative timeout",
			fsys: withNode("tool-a", managedFiles(replace(validManagedYAML, "scripts:\n", "timeouts:\n  remove: -5\nscripts:\n"))),
			want: "timeouts.remove must be positive",
		},
		{
			name: "unknown manifest field",
			fsys: withNode("tool-a", managedFiles(validManagedYAML+"flavour: spicy\n")),
			want: "field flavour not found",
		},
		{
			name: "missing manifest",
			fsys: withNode("tool-a", map[string]string{"install.sh": scriptBody}),
			want: "tool-a: read dependency.yaml",
		},
		{
			name: "empty manifest",
			fsys: withNode("tool-a", managedFiles("")),
			want: "manifest is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := LoadFS(tt.fsys)
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
	files := managedFiles(validManagedYAML)
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

func TestGetReturnsCopies(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	dep, _ := c.Get("codex")
	dep.Provides[0] = "mutated"
	dep.Platforms[0].Arch[0] = "mutated"
	fresh := c.MustGet("codex")
	if fresh.Provides[0] != "codex" || fresh.Platforms[0].Arch[0] != "amd64" {
		t.Fatalf("catalog state mutated through Get: %+v", fresh)
	}
}

func TestMustGetPanicsOnUnknownID(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustGet(ghost) should panic")
		}
	}()
	c.MustGet("ghost")
}
