package tools

import (
	"testing"

	"github.com/memohai/memoh/internal/hooks"
	"github.com/memohai/memoh/internal/workspace/bridge"
)

// resolveToolPath with no project must be byte-identical to normalizePath —
// the regression net for every session that has no project bound.
func TestResolveToolPathWithoutProjectMatchesNormalizePath(t *testing.T) {
	workspace := toolWorkspace{defaultWorkDir: "/data"}
	for _, value := range []string{"", "a.txt", "dir/a.txt", "/data/a.txt", "/data", "/etc/hosts"} {
		if got, want := workspace.resolveToolPath(value), workspace.normalizePath(value); got != want {
			t.Fatalf("resolveToolPath(%q) = %q, normalizePath = %q — must be identical without a project", value, got, want)
		}
	}
}

func TestResolveToolPathProjectRelative(t *testing.T) {
	workspace := toolWorkspace{defaultWorkDir: "/data", projectWorkDir: "/data/proj"}
	for name, tc := range map[string]struct {
		value string
		want  string
	}{
		// Relative paths land in the project, expressed workspace-relative
		// so the bridge server re-joins its own root onto them.
		"relative file":   {"a.txt", "proj/a.txt"},
		"relative nested": {"dir/a.txt", "proj/dir/a.txt"},
		"dot":             {".", "proj"},
		// Absolute paths must NOT be joined again — the double-join is the
		// silent-relocation bug this split exists to prevent.
		"absolute in project":  {"/data/proj/a.txt", "proj/a.txt"},
		"absolute in root":     {"/data/other.txt", "other.txt"},
		"absolute outside":     {"/etc/hosts", "/etc/hosts"},
		"workspace root alias": {"/data", "."},
	} {
		t.Run(name, func(t *testing.T) {
			if got := workspace.resolveToolPath(tc.value); got != tc.want {
				t.Fatalf("resolveToolPath(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestResolveToolPathProjectRelativeWindows(t *testing.T) {
	workspace := toolWorkspace{
		defaultWorkDir: `C:\Users\alice`,
		projectWorkDir: `C:\Users\alice\proj`,
		windows:        true,
	}
	for name, tc := range map[string]struct {
		value string
		want  string
	}{
		// normalizePath's windows branch emits forward-slash relative
		// remainders (the bridge server's filepath.Join accepts both); the
		// project join keeps that convention.
		"relative file":      {"a.txt", `proj/a.txt`},
		"relative forward":   {"dir/a.txt", `proj/dir/a.txt`},
		"absolute drive":     {`C:\Users\alice\other.txt`, "other.txt"},
		"drive qualified":    {`D:\elsewhere\x`, `D:\elsewhere\x`},
		"unc path":           {`\\server\share\x`, `\\server\share\x`},
		"rooted backslash":   {`\Users\alice\x`, `\Users\alice\x`},
		"drive no separator": {`C:stuff`, `C:stuff`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := workspace.resolveToolPath(tc.value); got != tc.want {
				t.Fatalf("resolveToolPath(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestHookWorkspaceInfoPrefersProjectWorkDir(t *testing.T) {
	target := resolvedToolTarget{
		info:      bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		workspace: toolWorkspace{defaultWorkDir: "/data", projectWorkDir: "/data/proj"},
	}
	if got := target.hookWorkspaceInfo("/data"); got.CWD != "/data/proj" {
		t.Fatalf("hook CWD = %q, want project dir", got.CWD)
	}
	noProject := resolvedToolTarget{
		info:      bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		workspace: toolWorkspace{defaultWorkDir: "/data"},
	}
	if got := noProject.hookWorkspaceInfo("/data"); got.CWD != "/data" {
		t.Fatalf("hook CWD without project = %q, want /data", got.CWD)
	}
	if hooks.DefaultWorkDir == "" {
		t.Fatal("hooks.DefaultWorkDir must not be empty")
	}
}

func TestToolWorkspaceFromInfoCarriesProjectWorkDir(t *testing.T) {
	info := bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"}
	workspace := toolWorkspaceFromInfo(info, "/data", " /data/proj ")
	if workspace.projectWorkDir != "/data/proj" {
		t.Fatalf("projectWorkDir = %q, want trimmed /data/proj", workspace.projectWorkDir)
	}
	if workspace.defaultWorkDir != "/data" {
		t.Fatalf("defaultWorkDir = %q — the project must never replace the strip root", workspace.defaultWorkDir)
	}
}
