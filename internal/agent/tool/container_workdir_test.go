package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/hooks"
	workspacepkg "github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// resolveToolPath with no workdir must be byte-identical to normalizePath —
// the regression net for every session that has no workdir bound.
func TestResolveToolPathWithoutWorkdirMatchesNormalizePath(t *testing.T) {
	workspace := toolWorkspace{defaultWorkDir: "/data"}
	for _, value := range []string{"", "a.txt", "dir/a.txt", "/data/a.txt", "/data", "/etc/hosts"} {
		if got, want := workspace.resolveToolPath(value), workspace.normalizePath(value); got != want {
			t.Fatalf("resolveToolPath(%q) = %q, normalizePath = %q — must be identical without a workdir", value, got, want)
		}
	}
}

func TestResolveToolPathWorkdirRelative(t *testing.T) {
	workspace := toolWorkspace{defaultWorkDir: "/data", workdirPath: "/data/proj"}
	for name, tc := range map[string]struct {
		value string
		want  string
	}{
		// Relative paths land in the workdir, expressed workspace-relative
		// so the bridge server re-joins its own root onto them.
		"relative file":   {"a.txt", "proj/a.txt"},
		"relative nested": {"dir/a.txt", "proj/dir/a.txt"},
		"dot":             {".", "proj"},
		// Absolute paths must NOT be joined again — the double-join is the
		// silent-relocation bug this split exists to prevent.
		"absolute in workdir":  {"/data/proj/a.txt", "proj/a.txt"},
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

func TestResolveToolPathWorkdirRelativeWindows(t *testing.T) {
	workspace := toolWorkspace{
		defaultWorkDir: `C:\Users\alice`,
		workdirPath:    `C:\Users\alice\proj`,
		windows:        true,
	}
	for name, tc := range map[string]struct {
		value string
		want  string
	}{
		// normalizePath's windows branch emits forward-slash relative
		// remainders (the bridge server's filepath.Join accepts both); the
		// workdir join keeps that convention.
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

func TestHookWorkspaceInfoPrefersWorkdirPath(t *testing.T) {
	target := resolvedToolTarget{
		info:      bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		workspace: toolWorkspace{defaultWorkDir: "/data", workdirPath: "/data/proj"},
	}
	if got := target.hookWorkspaceInfo("/data"); got.CWD != "/data/proj" {
		t.Fatalf("hook CWD = %q, want the working directory", got.CWD)
	}
	noWorkdir := resolvedToolTarget{
		info:      bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
		workspace: toolWorkspace{defaultWorkDir: "/data"},
	}
	if got := noWorkdir.hookWorkspaceInfo("/data"); got.CWD != "/data" {
		t.Fatalf("hook CWD without workdir = %q, want /data", got.CWD)
	}
	if hooks.DefaultWorkDir == "" {
		t.Fatal("hooks.DefaultWorkDir must not be empty")
	}
}

func TestToolWorkspaceFromInfoCarriesWorkdirPath(t *testing.T) {
	info := bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"}
	workspace := toolWorkspaceFromInfo(info, "/data", " /data/proj ")
	if workspace.workdirPath != "/data/proj" {
		t.Fatalf("workdirPath = %q, want trimmed /data/proj", workspace.workdirPath)
	}
	if workspace.defaultWorkDir != "/data" {
		t.Fatalf("defaultWorkDir = %q — the workdir must never replace the strip root", workspace.defaultWorkDir)
	}
}

// A workdir pins the session's execution location for its whole life. The
// chat request path already rejects a switch; the tool path must too, or the
// model can reach another computer through target_id while relative paths
// still point at the workdir directory — which exists only on the workdir's
// machine.
func TestResolveToolTargetRejectsTargetSwitchForWorkdirBoundSession(t *testing.T) {
	t.Parallel()

	targetProvider := &containerTestTargetProvider{resolved: workspacepkg.ResolvedWorkspaceTarget{
		TargetID: "other-computer",
		Client:   &bridge.Client{},
		Info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, DefaultWorkDir: "/workspace"},
	}}
	provider := NewContainerProvider(nil, targetProvider, nil, "")
	session := SessionContext{
		BotID:             "bot-1",
		WorkspaceTargetID: "workdir-computer",
		WorkdirPath:       "/data/proj",
	}
	_, err := provider.resolveToolTarget(context.Background(), session, map[string]any{"target_id": "other-computer"})
	if err == nil {
		t.Fatal("resolveToolTarget() with a foreign target_id must fail for a workdir-bound session")
	}
	for _, want := range []string{"working directory", "workdir-computer"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}
	if targetProvider.resolvedInput != "" {
		t.Fatalf("resolver was called with %q — the guard must run before resolution", targetProvider.resolvedInput)
	}
}

func TestResolveToolTargetAllowsPinnedTargetForWorkdirBoundSession(t *testing.T) {
	t.Parallel()

	targetProvider := &containerTestTargetProvider{resolved: workspacepkg.ResolvedWorkspaceTarget{
		TargetID: "workdir-computer",
		Client:   &bridge.Client{},
		Info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, DefaultWorkDir: "/workspace"},
	}}
	provider := NewContainerProvider(nil, targetProvider, nil, "")
	session := SessionContext{
		BotID:             "bot-1",
		WorkspaceTargetID: "workdir-computer",
		WorkdirPath:       "/workspace/proj",
	}
	resolved, err := provider.resolveToolTarget(context.Background(), session, map[string]any{"target_id": "workdir-computer"})
	if err != nil {
		t.Fatalf("resolveToolTarget() with the pinned target error = %v", err)
	}
	if resolved.workspace.workdirPath != "/workspace/proj" {
		t.Fatalf("workdirPath = %q, want the working directory on its own target", resolved.workspace.workdirPath)
	}
}

// Browser Use and Computer Use always run on the native Server Workspace and
// save screenshots there, so a remote-workdir session must still be able to
// read them back — but without dragging the (remote) workdir directory onto
// that machine.
func TestResolveToolTargetAllowsNativeReadbackWithoutWorkdirPath(t *testing.T) {
	t.Parallel()

	targetProvider := &containerTestTargetProvider{resolved: workspacepkg.ResolvedWorkspaceTarget{
		TargetID: workspacepkg.WorkspaceTargetNative,
		Client:   &bridge.Client{},
		Info:     bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"},
	}}
	provider := NewContainerProvider(nil, targetProvider, nil, "")
	session := SessionContext{
		BotID:             "bot-1",
		WorkspaceTargetID: "workdir-computer",
		WorkdirPath:       `C:\Users\alice\proj`,
	}
	resolved, err := provider.resolveToolTarget(context.Background(), session, map[string]any{"target_id": workspacepkg.WorkspaceTargetNative})
	if err != nil {
		t.Fatalf("resolveToolTarget() to the native workspace error = %v", err)
	}
	if resolved.workspace.workdirPath != "" {
		t.Fatalf("workdirPath = %q — the working directory does not exist on the native workspace", resolved.workspace.workdirPath)
	}
	if resolved.workspace.defaultWorkDir != "/data" {
		t.Fatalf("defaultWorkDir = %q, want the native workspace root", resolved.workspace.defaultWorkDir)
	}
}

func TestWorkdirBoundToolDescriptionsTellTheModelTargetIsPinned(t *testing.T) {
	t.Parallel()

	provider := NewContainerProvider(nil, nil, nil, "")
	bound := provider.workspaceTargetParameter(SessionContext{WorkdirPath: "/data/proj"})["description"].(string)
	if !strings.Contains(bound, "omit this parameter") {
		t.Fatalf("workdir-bound target_id description = %q, want it to ask for omission", bound)
	}
	unbound := provider.workspaceTargetParameter(SessionContext{})["description"].(string)
	if strings.Contains(unbound, "bound to a workdir") {
		t.Fatalf("unbound target_id description must not mention a workdir, got %q", unbound)
	}

	usage := provider.Usage(context.Background(), SessionContext{
		WorkspaceTargetID: "workdir-computer",
		WorkdirPath:       "/data/proj",
	}, availableToolsForTest(ToolRead(), ToolExec(), ToolListExecutionLocations()))
	if strings.Contains(usage, "An explicit `target_id` still takes precedence") {
		t.Fatalf("workdir-bound usage must not claim target_id wins, got:\n%s", usage)
	}
	if !strings.Contains(usage, "pins") {
		t.Fatalf("workdir-bound usage should say the location is pinned, got:\n%s", usage)
	}
}
