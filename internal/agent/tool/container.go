package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	pathpkg "path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/hooks"
	workspacepkg "github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// blockedSleepPattern matches standalone `sleep N` where N >= 2.
// Does not match sleep inside pipelines, subshells, or scripts.
var blockedSleepPattern = regexp.MustCompile(`^sleep\s+(\d+(?:\.\d+)?)(?:\s*[;&]|$)`)

const (
	defaultContainerExecWorkDir  = "/data"
	remoteBackgroundOutputLogDir = "/data/.memoh/background"
)

// containerOpTimeout is the maximum time allowed for individual file
// operations (read, write, list, edit). Exec has its own timeout.
const containerOpTimeout = 30 * time.Second

// largeFileThreshold defines the size above which file operations use
// streaming (async chunked I/O) instead of loading fully into memory.
// Files <= this threshold use the simpler synchronous gRPC calls.
const largeFileThreshold = 512 * 1024 // 512 KB

type ContainerProvider struct {
	clients     bridge.Provider
	bgManager   *background.Manager
	hookService workspaceHookService
	execWorkDir string
	logger      *slog.Logger
}

type workspaceHookService interface {
	Run(ctx context.Context, req hooks.Request, runner hooks.ToolRunner) (hooks.Result, error)
}

func NewContainerProvider(log *slog.Logger, clients bridge.Provider, bgManager *background.Manager, execWorkDir string, hookServices ...*hooks.Service) *ContainerProvider {
	if log == nil {
		log = slog.Default()
	}
	wd := strings.TrimSpace(execWorkDir)
	if wd == "" {
		wd = defaultContainerExecWorkDir
	}
	var hookService *hooks.Service
	if len(hookServices) > 0 {
		hookService = hookServices[0]
	}
	return &ContainerProvider{clients: clients, bgManager: bgManager, hookService: hookService, execWorkDir: wd, logger: log.With(slog.String("tool", "container"))}
}

func (p *ContainerProvider) SetHookService(h *hooks.Service) {
	p.hookService = h
}

func (*ContainerProvider) Usage(_ context.Context, session SessionContext, available AvailableTools) string {
	var parts []string
	locationRef, hasLocationTool := available.Ref(ToolListExecutionLocations())
	if hasLocationTool {
		parts = append(parts, locationRef+": list the current Server Workspace and connected computers available to this Bot for file and command work")
	}
	if targetID := strings.TrimSpace(session.WorkspaceTargetID); targetID != "" {
		targetDescription := "request-selected execution location"
		if sessionUsesRemoteWorkspaceTarget(session) {
			targetDescription = "request-selected connected computer"
		} else if strings.EqualFold(strings.TrimSpace(session.WorkspaceTargetKind), workspacepkg.WorkspaceTargetNative) || strings.EqualFold(targetID, workspacepkg.WorkspaceTargetNative) {
			targetDescription = "native Server Workspace"
		}
		if name := strings.TrimSpace(session.WorkspaceTargetName); name != "" {
			targetDescription += " " + strconv.Quote(name)
		}
		text := "File and command tools default to the " + targetDescription + " for this turn"
		if sessionIsWorkdirBound(session) {
			// The bound working directory pins the location for the session's
			// whole life, so the model must not be told target_id can move it.
			text += ". The chat's working directory pins this location: `target_id` cannot move file or command tools to another computer"
		} else {
			text += ". An explicit `target_id` still takes precedence"
		}
		parts = append(parts, text+".")
	}
	if workDir := strings.TrimSpace(session.WorkdirPath); workDir != "" {
		parts = append(parts, "This chat's working directory is "+strconv.Quote(workDir)+". Relative file paths resolve inside it, and commands run there by default.")
	}
	if ref, ok := available.Ref(ToolRead()); ok {
		text := ref + ": read file content"
		if session.SupportsImageInput {
			text += " (also supports images: PNG, JPEG, GIF, WebP)"
		}
		parts = append(parts, text)
	}
	if ref, ok := available.Ref(ToolWrite()); ok {
		parts = append(parts, ref+": write file content")
	}
	if ref, ok := available.Ref(ToolList()); ok {
		parts = append(parts, ref+": list directory entries")
	}
	if ref, ok := available.Ref(ToolEdit()); ok {
		parts = append(parts, ref+": replace exact text in a file")
	}
	if ref, ok := available.Ref(ToolApplyPatch()); ok {
		parts = append(parts, ref+": apply a patch to files")
	}
	if ref, ok := available.Ref(ToolExec()); ok {
		parts = append(parts, ref+": execute command")
	}
	if hasLocationTool && len(available.Refs(ToolRead(), ToolWrite(), ToolList(), ToolEdit(), ToolApplyPatch(), ToolExec())) > 0 {
		if sessionIsWorkdirBound(session) {
			parts = append(parts, "Use "+locationRef+" only to report which locations exist or to check availability: this chat's working directory pins its location. File and command tools must omit `target_id`; the one other accepted value is `native`, for reading files Browser Use and Computer Use leave on the Server Workspace. Any other location's `target_id` fails.")
		} else {
			defaultDescription := "the Bot's default"
			if strings.TrimSpace(session.WorkspaceTargetID) != "" {
				defaultDescription = "the request-selected default for this turn"
			}
			parts = append(parts, "Use "+locationRef+" when the user names a computer, you need a non-default location, availability may have changed, or a target call fails. Pass its `target_id` to file and command tools; omit `target_id` to use "+defaultDescription+". Listing locations does not switch locations or folders.")
		}
	}
	if sessionUsesRemoteWorkspaceTarget(session) {
		displayRefs := available.Refs(ToolBrowserObserve(), ToolBrowserAction(), ToolBrowserRemoteSession(), ToolComputerObserve(), ToolComputerAction())
		if len(displayRefs) > 0 {
			text := "The request-selected connected computer is the default only for file and command tools. Browser Use and Computer Use (" + strings.Join(displayRefs, ", ") + ") remain on the native Server Workspace and do not follow that default."
			if readRef, ok := available.Ref(ToolRead()); ok {
				text += " Use " + readRef + " with `target_id` `native` to read screenshots or other files those tools create there."
			}
			parts = append(parts, text)
		}
	}
	return usageSection("Basic Tools", parts)
}

func (p *ContainerProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
	workspace := p.resolveToolWorkspace(ctx, session)
	// Tool descriptions tell the model where relative paths land; with a
	// workdir bound, that is the working directory.
	wd := workspace.defaultWorkDir
	if workspace.workdirPath != "" {
		wd = workspace.workdirPath
	}
	sess := session
	targetParameter := p.workspaceTargetParameter(sess)

	readDesc := fmt.Sprintf("Read file content %s. Reads the full file by default; use line_offset and n_lines for pagination. Files up to ~16 MB are supported.", workspace.locationDescription)
	if sess.SupportsImageInput {
		readDesc += " Also supports reading image files (PNG, JPEG, GIF, WebP) — binary images are loaded into model context automatically."
	}

	toolList := []sdk.Tool{
		{
			Name:        ToolRead().String(),
			Description: readDesc,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target_id":   targetParameter,
					"path":        map[string]any{"type": "string", "description": fmt.Sprintf("File path (relative to %s or absolute %s)", wd, workspace.absolutePathDescription)},
					"line_offset": map[string]any{"type": "integer", "description": "Line number to start reading from (1-indexed). Default: 1.", "minimum": 1, "default": 1},
					"n_lines":     map[string]any{"type": "integer", "description": "Number of lines to read. Default: read entire file.", "minimum": 1},
				},
				"required": []string{"path"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execRead(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolWrite().String(),
			Description: fmt.Sprintf("Write file content %s. Creates parent directories automatically. Handles files of any size.", workspace.locationDescription),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target_id": targetParameter,
					"path":      map[string]any{"type": "string", "description": fmt.Sprintf("File path (relative to %s or absolute %s)", wd, workspace.absolutePathDescription)},
					"content":   map[string]any{"type": "string", "description": "File content"},
				},
				"required": []string{"path", "content"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execWrite(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolList().String(),
			Description: fmt.Sprintf("List directory entries %s. Supports pagination. Max %d entries per call. In recursive mode, subdirectories with >%d items are collapsed to a summary.", workspace.locationDescription, listMaxEntries, listCollapseThreshold),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target_id": targetParameter,
					"path":      map[string]any{"type": "string", "description": fmt.Sprintf("Directory path (relative to %s or absolute %s)", wd, workspace.absolutePathDescription)},
					"recursive": map[string]any{"type": "boolean", "description": "List recursively"},
					"offset":    map[string]any{"type": "integer", "description": "Entry offset to start from (0-indexed). Default: 0.", "minimum": 0, "default": 0},
					"limit":     map[string]any{"type": "integer", "description": fmt.Sprintf("Max entries to return per call. Default: %d. Max: %d.", listMaxEntries, listMaxEntries), "minimum": 1, "maximum": listMaxEntries, "default": listMaxEntries},
				},
				"required": []string{"path"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execList(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolEdit().String(),
			Description: fmt.Sprintf("Replace exact text in a file %s.", workspace.locationDescription),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target_id": targetParameter,
					"path":      map[string]any{"type": "string", "description": fmt.Sprintf("File path (relative to %s or absolute %s)", wd, workspace.absolutePathDescription)},
					"old_text":  map[string]any{"type": "string", "description": "Exact text to find"},
					"new_text":  map[string]any{"type": "string", "description": "Replacement text"},
				},
				"required": []string{"path", "old_text", "new_text"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execEdit(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name: ToolApplyPatch().String(),
			Description: fmt.Sprintf(`Apply a structured patch %s. This is a Memoh/Codex-style patch format, not a standard unified diff or git patch.

Use this tool for multi-file edits, structured code changes, file creation, file deletion, or file moves.

Patch grammar:
- The patch must start with "*** Begin Patch" and end with "*** End Patch".
- Add a file with "*** Add File: path". Every following content line for that file must start with "+".
- Delete a file with "*** Delete File: path".
- Update a file with "*** Update File: path".
- Move or rename a file by putting "*** Move to: new/path" immediately after an update header, before any changed lines.
- Inside update hunks, use "@@" or "@@ context line" before changed lines. The optional context line helps locate the block in the file.
- Changed lines use a one-character prefix: " " for unchanged context, "-" for removed lines, and "+" for added lines.
- Use "*** End of File" inside an update hunk when the hunk should match the end of the file.
- Paths are relative to the workspace by default. Absolute paths are accepted only when the workspace backend allows them.

Examples:

Add a file:
*** Begin Patch
*** Add File: docs/notes.md
+new line
*** End Patch

Update a file:
*** Begin Patch
*** Update File: path
@@
-old line
+new line
*** End Patch

Move a file:
*** Begin Patch
*** Update File: old/path.txt
*** Move to: new/path.txt
@@
 old content
*** End Patch

Delete a file:
*** Begin Patch
*** Delete File: obsolete.txt
*** End Patch
`, workspace.locationDescription),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target_id": targetParameter,
					"patch":     map[string]any{"type": "string", "description": "Patch body using the apply_patch format. Paths are relative to the workspace by default, or absolute paths supported by the workspace backend."},
				},
				"required": []string{"patch"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execApplyPatch(ctx.Context, sess, input)
			},
		},
		{
			Name: ToolExec().String(),
			Description: fmt.Sprintf(`Execute a %s command %s. Runs in %s by default.

# Instructions
%s
- Use this tool to run shell commands for installing packages, running scripts, building code, running tests, and other system operations.
- If your command will take a long time (package installs, builds, test suites), set run_in_background to true. The call returns a task ID immediately. You do not need to add '&' at the end of the command when using this parameter.
- If waiting for a background task, use wait_until(task_id): it returns with a reason (completed/failed/killed/stalled/idle/timeout) and the latest output_tail. Then use get_background_status(task_id) to inspect result.
- For processes that never exit on their own (dev servers, watch mode), use run_in_background, then wait_until(task_id): once output settles it returns with reason 'idle' — check output_tail for the ready message (e.g. a local URL) and proceed. Do not wait for such processes to complete.
- You may specify a custom timeout (up to %d seconds) for commands you know will take longer than the default %d seconds. If a foreground command times out, it will be automatically moved to the background; use wait_until(task_id), then get_background_status(task_id).
- Avoid unnecessary delay commands:
  - Do not sleep between commands that can run immediately — just run them.
  - If your command is long running, use run_in_background. No delay needed.
  - Do not retry failing commands in a delay loop — diagnose the root cause.
  - If waiting for a background task, use wait_until(task_id).
%s`, workspace.shellDescription, workspace.locationDescription, wd, workspace.platformInstructions, background.MaxExecTimeout, background.DefaultExecTimeout, workspace.delayInstruction),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target_id":         targetParameter,
					"command":           map[string]any{"type": "string", "description": fmt.Sprintf("Command to run (e.g. %s)", workspace.commandExamples)},
					"work_dir":          map[string]any{"type": "string", "description": fmt.Sprintf("Working directory (default: %s)", wd)},
					"description":       map[string]any{"type": "string", "description": workspace.descriptionExamples},
					"timeout":           map[string]any{"type": "integer", "description": fmt.Sprintf("Timeout in seconds (default: %d, max: %d). Only applies to foreground execution. Commands that exceed this timeout are automatically moved to background.", background.DefaultExecTimeout, background.MaxExecTimeout), "minimum": 1, "maximum": background.MaxExecTimeout},
					"run_in_background": map[string]any{"type": "boolean", "description": "If true, run the command in the background. Returns immediately with a task ID. Use wait_until(task_id), then get_background_status(task_id) to inspect result. Use for long-running commands (installs, builds, test suites) and for processes that never exit (dev servers, watch mode). You do not need to use '&' at the end of the command."},
				},
				"required": []string{"command"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execExec(ctx.Context, sess, inputAsMap(input))
			},
		},
	}
	if resolver, ok := p.clients.(workspaceTargetResolver); ok {
		locationTool := sdk.Tool{
			Name: ToolListExecutionLocations().String(),
			Description: "List the execution locations configured for this Bot for file operations and command execution, with current availability and status. " +
				"The default field identifies the current turn's default (the request-selected target when present, otherwise the Bot's Primary). " +
				"Use the returned target_id with file and command tools when a non-default location is needed. " +
				"The available field says whether a location can currently be used. This tool does not change the default location or starting folder.",
			Parameters: emptyObjectSchema(),
			Execute: func(ctx *sdk.ToolExecContext, _ any) (any, error) {
				ctx.Context = workspaceContextForSession(ctx.Context, sess)
				targets, err := resolver.ListWorkspaceTargets(ctx.Context, sess.BotID)
				if err != nil {
					return nil, fmt.Errorf("list execution locations: %w", err)
				}
				locations := make([]executionLocation, 0, len(targets))
				for _, target := range targets {
					if strings.TrimSpace(target.TargetID) == "" {
						continue
					}
					locations = append(locations, executionLocationFromTarget(target, sess.WorkspaceTargetID))
				}
				return listExecutionLocationsResult{Locations: locations}, nil
			},
		}
		toolList = append([]sdk.Tool{locationTool}, toolList...)
	}
	return toolList, nil
}

type toolWorkspace struct {
	defaultWorkDir string
	// workdirPath is the session's working directory. It changes where
	// relative tool paths land and what cwd exec defaults to; it never
	// replaces defaultWorkDir, which stays the workspace root that
	// normalizePath strips (the bridge server re-joins its own root, so
	// moving the strip prefix would silently relocate files).
	workdirPath             string
	locationDescription     string
	absolutePathDescription string
	shellDescription        string
	commandExamples         string
	descriptionExamples     string
	platformInstructions    string
	delayInstruction        string
	windows                 bool
}

type resolvedToolTarget struct {
	id        string
	client    *bridge.Client
	info      bridge.WorkspaceInfo
	workspace toolWorkspace
}

func (t resolvedToolTarget) hookWorkspaceInfo(fallbackWorkDir string) hooks.WorkspaceInfo {
	info := hookWorkspaceInfoFromBridge(t.info, fallbackWorkDir)
	// Hooks observe the directory tools actually work in, which the
	// session's workdir overrides.
	if t.workspace.workdirPath != "" {
		info.CWD = t.workspace.workdirPath
	}
	return info
}

func (t resolvedToolTarget) backgroundOutputDir() string {
	if strings.EqualFold(strings.TrimSpace(t.info.Backend), bridge.WorkspaceBackendRemote) {
		return remoteBackgroundOutputLogDir
	}
	return background.OutputLogDir
}

type workspaceTargetResolver interface {
	ResolveWorkspaceTarget(ctx context.Context, botID, targetID string) (workspacepkg.ResolvedWorkspaceTarget, error)
	ListWorkspaceTargets(ctx context.Context, botID string) ([]workspacepkg.WorkspaceTarget, error)
}

type executionLocation struct {
	TargetID  string `json:"target_id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Default   bool   `json:"default"`
	Available bool   `json:"available"`
	Status    string `json:"status"`
}

type listExecutionLocationsResult struct {
	Locations []executionLocation `json:"locations"`
}

func (p *ContainerProvider) resolveToolWorkspace(ctx context.Context, session SessionContext) toolWorkspace {
	return toolWorkspaceFromInfo(p.resolveToolWorkspaceInfo(ctx, session), p.execWorkDir, session.WorkdirPath)
}

func (p *ContainerProvider) resolveToolWorkspaceInfo(ctx context.Context, session SessionContext) bridge.WorkspaceInfo {
	ctx = workspaceContextForSession(ctx, session)
	info := bridge.WorkspaceInfo{
		Backend:        bridge.WorkspaceBackendContainer,
		DefaultWorkDir: p.execWorkDir,
	}
	if resolver, ok := p.clients.(bridge.WorkspaceInfoProvider); ok {
		if resolved, err := resolver.WorkspaceInfo(ctx, session.BotID); err == nil {
			info = resolved
		}
	}
	return info
}

func toolWorkspaceFromInfo(info bridge.WorkspaceInfo, fallbackWorkDir, workdirPath string) toolWorkspace {
	wd := strings.TrimSpace(info.DefaultWorkDir)
	if wd == "" {
		wd = fallbackWorkDir
	}
	workspace := toolWorkspace{
		defaultWorkDir:          wd,
		workdirPath:             strings.TrimSpace(workdirPath),
		locationDescription:     "inside the bot workspace",
		absolutePathDescription: "inside the workspace",
		shellDescription:        "shell",
		commandExamples:         "ls -la, npm install, python script.py",
		descriptionExamples:     `Clear, concise description of what this command does in active voice. For simple commands keep it brief (5-10 words): ls -la → "List files with details". For complex commands add enough context: curl -s url | jq '.data[]' → "Fetch JSON and extract data array".`,
		delayInstruction:        "  - sleep N (N >= 2) in foreground is blocked. If you genuinely need a short delay, keep it under 2 seconds.",
	}
	if strings.EqualFold(info.Backend, bridge.WorkspaceBackendRemote) {
		workspace.locationDescription = "on the connected remote machine"
		workspace.absolutePathDescription = "remote workspace path"
		if strings.EqualFold(info.OS, "win32") {
			workspace.windows = true
			workspace.shellDescription = "Windows Command Prompt (cmd.exe)"
			workspace.commandExamples = "dir, npm install, python script.py"
			workspace.descriptionExamples = `Clear, concise description of what this command does in active voice. For simple commands keep it brief (5-10 words): dir → "List files and folders". For complex commands add enough context to explain the operation and expected result.`
			workspace.platformInstructions = `- This remote machine uses Windows Command Prompt (cmd.exe). Use Windows command syntax and commands such as dir, type, and where; do not assume POSIX commands such as ls, cat, pwd, or sleep are installed.
- Do not use start to detach a command. Use run_in_background so the Runtime retains ownership of the process tree.`
			workspace.delayInstruction = "  - Do not use timeout /t or ping loops to wait for background work; use wait_until."
		}
	}
	return workspace
}

func (*ContainerProvider) workspaceTargetParameter(session SessionContext) map[string]any {
	description := "Exact target_id returned by list_execution_locations. Do not pass a location name, type, or runtime ID. Omit to use the default location for the current turn."
	if sessionIsWorkdirBound(session) {
		// A bound session's directory only exists on one machine, so the
		// pinned target is the only accepted value — say so here as well as
		// rejecting it at execution time.
		description = "Exact target_id returned by list_execution_locations. This chat has a fixed working directory, which pins its execution location: omit this parameter. The only other accepted value is native, for reading files Browser Use and Computer Use leave on the Server Workspace; any other target_id is rejected."
	}
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}

// sessionIsWorkdirBound reports whether the session derives its working
// directory from a bot workdir. The resolved path is the binding's only
// per-session trace in tool land, so it doubles as the binding flag.
func sessionIsWorkdirBound(session SessionContext) bool {
	return strings.TrimSpace(session.WorkdirPath) != ""
}

// ensureWorkdirPinnedTarget rejects an explicit tool target that would move a
// workdir-bound session off the location its workdir pins. The chat request
// path rejects the same switch (ErrWorkspaceTargetWorkdirConflict); without
// this guard the tool path bypasses it, running commands on another computer
// while relative paths still resolve against the working directory — a
// directory that exists only on the workdir's machine.
func ensureWorkdirPinnedTarget(session SessionContext, requestedTargetID string) error {
	requested := strings.TrimSpace(requestedTargetID)
	if requested == "" || !sessionIsWorkdirBound(session) {
		return nil
	}
	pinned := strings.TrimSpace(session.WorkspaceTargetID)
	if strings.EqualFold(pinned, requested) {
		return nil
	}
	// The native Server Workspace stays reachable: Browser Use and Computer
	// Use always run there and save screenshots there, so even a session
	// pinned to a remote workdir must be able to read them back. Those reads
	// resolve against the native workspace root, never the working directory
	// — see workdirPathForTarget.
	if strings.EqualFold(requested, workspacepkg.WorkspaceTargetNative) {
		return nil
	}
	if pinned == "" {
		return errors.New("this chat has a fixed working directory, which pins its execution location; omit target_id")
	}
	return fmt.Errorf("this chat's working directory lives on execution location %q, which pins it for the whole chat; omit target_id (or pass %q)", pinned, pinned)
}

// workdirPathForTarget returns the working directory only for the target the
// workdir actually lives on. The native escape hatch above lands on a
// different machine, where that directory does not exist; carrying it there
// would resolve every relative path against a phantom directory.
func workdirPathForTarget(session SessionContext, resolvedTargetID string) string {
	workDir := strings.TrimSpace(session.WorkdirPath)
	pinned := strings.TrimSpace(session.WorkspaceTargetID)
	resolved := strings.TrimSpace(resolvedTargetID)
	if workDir == "" || pinned == "" || resolved == "" || strings.EqualFold(pinned, resolved) {
		return workDir
	}
	return ""
}

func executionLocationFromTarget(target workspacepkg.WorkspaceTarget, requestTargetID string) executionLocation {
	status := strings.TrimSpace(target.Status)
	if status == "" {
		if target.Online {
			status = workspacepkg.WorkspaceTargetStatusOnline
		} else {
			status = workspacepkg.WorkspaceTargetStatusOffline
		}
	}
	name := strings.TrimSpace(target.Name)
	targetType := "execution_location"
	switch target.Kind {
	case workspacepkg.WorkspaceTargetNative:
		targetType = "server_workspace"
		if name == "" {
			name = "Server Workspace"
		}
	case workspacepkg.WorkspaceTargetRemote:
		targetType = "connected_computer"
		if name == "" {
			name = "Unavailable connected computer"
		}
	}
	requestTargetID = strings.TrimSpace(requestTargetID)
	isDefault := target.Primary
	if requestTargetID != "" {
		isDefault = strings.TrimSpace(target.TargetID) == requestTargetID
	}
	return executionLocation{
		TargetID:  strings.TrimSpace(target.TargetID),
		Name:      name,
		Type:      targetType,
		Default:   isDefault,
		Available: status == workspacepkg.WorkspaceTargetStatusOnline,
		Status:    status,
	}
}

func (p *ContainerProvider) resolveToolTarget(ctx context.Context, session SessionContext, args map[string]any) (resolvedToolTarget, error) {
	ctx = workspaceContextForSession(ctx, session)
	targetID := StringArg(args, "target_id")
	if err := ensureWorkdirPinnedTarget(session, targetID); err != nil {
		return resolvedToolTarget{}, err
	}
	if resolver, ok := p.clients.(workspaceTargetResolver); ok {
		resolved, err := resolver.ResolveWorkspaceTarget(ctx, session.BotID, targetID)
		if err != nil {
			if errors.Is(err, workspacepkg.ErrWorkspaceTargetNotFound) {
				return resolvedToolTarget{}, fmt.Errorf("execution location target_id is invalid or is no longer configured for this Bot; call %s and retry with a returned target_id: %w", ToolListExecutionLocations().String(), err)
			}
			return resolvedToolTarget{}, fmt.Errorf("workspace target is not reachable: %w", err)
		}
		if resolved.Client == nil {
			return resolvedToolTarget{}, errors.New("workspace target is not reachable: client is unavailable")
		}
		// Keep the canonical target on the original input map. Besides making
		// retries deterministic if the Bot Primary changes, this also leaves an
		// unambiguous target_id in the persisted tool-call history.
		if args != nil {
			args["target_id"] = strings.TrimSpace(resolved.TargetID)
		}
		return resolvedToolTarget{
			id:        resolved.TargetID,
			client:    resolved.Client,
			info:      resolved.Info,
			workspace: toolWorkspaceFromInfo(resolved.Info, p.execWorkDir, workdirPathForTarget(session, resolved.TargetID)),
		}, nil
	}
	if targetID != "" {
		return resolvedToolTarget{}, errors.New("workspace target selection is not supported")
	}
	client, err := p.getClient(ctx, session.BotID)
	if err != nil {
		return resolvedToolTarget{}, err
	}
	info := p.resolveToolWorkspaceInfo(ctx, session)
	return resolvedToolTarget{
		client:    client,
		info:      info,
		workspace: toolWorkspaceFromInfo(info, p.execWorkDir, session.WorkdirPath),
	}, nil
}

func workspaceContextForSession(ctx context.Context, session SessionContext) context.Context {
	targetID := strings.TrimSpace(session.WorkspaceTargetID)
	if targetID == "" {
		return ctx
	}
	return workspacepkg.WithWorkspaceTarget(ctx, targetID)
}

func sessionUsesRemoteWorkspaceTarget(session SessionContext) bool {
	kind := strings.TrimSpace(session.WorkspaceTargetKind)
	if kind != "" {
		return strings.EqualFold(kind, workspacepkg.WorkspaceTargetRemote)
	}
	targetID := strings.TrimSpace(session.WorkspaceTargetID)
	return targetID != "" && !strings.EqualFold(targetID, workspacepkg.WorkspaceTargetNative)
}

func hookWorkspaceInfoFromBridge(info bridge.WorkspaceInfo, fallbackWorkDir string) hooks.WorkspaceInfo {
	cwd := strings.TrimSpace(info.DefaultWorkDir)
	if cwd == "" {
		cwd = strings.TrimSpace(fallbackWorkDir)
	}
	if cwd == "" {
		cwd = hooks.DefaultWorkDir
	}
	runtime := strings.TrimSpace(info.Backend)
	if runtime == "" {
		runtime = bridge.WorkspaceBackendContainer
	}
	return hooks.WorkspaceInfo{CWD: cwd, Runtime: runtime}
}

func (p *ContainerProvider) runWorkspaceToolHook(ctx context.Context, session SessionContext, workspace hooks.WorkspaceInfo, eventName string, extra map[string]any) (hooks.Result, error) {
	if p == nil || p.hookService == nil {
		return hooks.Result{Decision: hooks.DecisionAllow, RuntimeSupported: hooks.RuntimeSupported(eventName)}, nil
	}
	req := hooks.Request{
		Version:   1,
		Event:     eventName,
		BotID:     session.BotID,
		SessionID: session.SessionID,
		ChatID:    session.ChatID,
		Workspace: workspace,
		Extra:     extra,
	}
	return p.hookService.Run(ctx, req, nil)
}

func (p *ContainerProvider) logWorkspaceToolHookError(eventName, botID, sessionID string, err error) {
	if p == nil || p.logger == nil || err == nil {
		return
	}
	p.logger.Warn("workspace tool hook failed",
		slog.String("event", eventName),
		slog.String("bot_id", botID),
		slog.String("session_id", sessionID),
		slog.Any("error", err),
	)
}

// resolveToolPath is the tool-facing path resolver: a relative path lands in
// the session's working directory (when one is bound) before the usual
// workspace-root prefix stripping. normalizePath itself must stay untouched —
// it strips the root the bridge server re-joins, and pointing it at the
// working directory would silently relocate files outside it.
func (w toolWorkspace) resolveToolPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || w.workdirPath == "" || w.isAbsToolPath(value) {
		return w.normalizePath(value)
	}
	if w.windows {
		joined := strings.TrimRight(w.workdirPath, `\`) + `\` + strings.ReplaceAll(value, "/", `\`)
		return w.normalizePath(joined)
	}
	return w.normalizePath(pathpkg.Join(w.workdirPath, value))
}

// isAbsToolPath reports whether value is absolute for the workspace's OS.
// Windows treats drive-qualified (C:...) and UNC (\\host\share) paths as
// absolute; joining either onto a working directory would produce garbage.
func (w toolWorkspace) isAbsToolPath(value string) bool {
	if w.windows {
		normalized := strings.ReplaceAll(value, "/", `\`)
		if strings.HasPrefix(normalized, `\\`) || strings.HasPrefix(normalized, `\`) {
			return true
		}
		return len(normalized) >= 2 && normalized[1] == ':'
	}
	return strings.HasPrefix(value, "/")
}

func (w toolWorkspace) normalizePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	prefix := strings.TrimSpace(w.defaultWorkDir)
	if prefix == "" {
		prefix = defaultContainerExecWorkDir
	}
	if w.windows {
		valueForCompare := strings.ReplaceAll(value, `\`, "/")
		prefixForCompare := strings.TrimRight(strings.ReplaceAll(prefix, `\`, "/"), "/")
		if strings.EqualFold(valueForCompare, prefixForCompare) {
			return "."
		}
		if len(valueForCompare) > len(prefixForCompare) &&
			strings.EqualFold(valueForCompare[:len(prefixForCompare)], prefixForCompare) &&
			valueForCompare[len(prefixForCompare)] == '/' {
			return strings.TrimLeft(valueForCompare[len(prefixForCompare)+1:], "/")
		}
		return value
	}
	prefix = strings.TrimRight(prefix, "/")
	if value == prefix {
		return "."
	}
	if strings.HasPrefix(value, prefix+"/") {
		return strings.TrimLeft(strings.TrimPrefix(value, prefix+"/"), "/")
	}
	return value
}

func (p *ContainerProvider) getClient(ctx context.Context, botID string) (*bridge.Client, error) {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	client, err := p.clients.MCPClient(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("workspace is not reachable: %w", err)
	}
	return client, nil
}

func (p *ContainerProvider) execRead(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	opCtx, opCancel := context.WithTimeout(ctx, containerOpTimeout)
	defer opCancel()

	target, err := p.resolveToolTarget(opCtx, session, args)
	if err != nil {
		return nil, err
	}
	client := target.client
	filePath := target.workspace.resolveToolPath(StringArg(args, "path"))
	if filePath == "" {
		return nil, errors.New("path is required")
	}

	lineOffset := 1
	if offset, ok, err := IntArg(args, "line_offset"); err != nil {
		return nil, fmt.Errorf("invalid line_offset: %w", err)
	} else if ok {
		if offset < 1 {
			return nil, errors.New("line_offset must be >= 1")
		}
		lineOffset = offset
	}
	nLines := 0 // 0 = read entire file
	if n, ok, err := IntArg(args, "n_lines"); err != nil {
		return nil, fmt.Errorf("invalid n_lines: %w", err)
	} else if ok && n > 0 {
		nLines = n
	}

	// Pre-check file size to avoid loading excessively large files into
	// memory. The gRPC transport is capped at 16 MB, so anything larger
	// would fail anyway; reject early with a clear message.
	const maxReadBytes = 16 * 1024 * 1024 // 16 MB
	if stat, err := client.Stat(opCtx, filePath); err == nil && stat != nil {
		if stat.GetSize() > maxReadBytes {
			return nil, fmt.Errorf("file is too large (%d bytes, limit %d bytes). The read tool cannot read files above this limit", stat.GetSize(), maxReadBytes)
		}
	}

	// Stream-read the full file content.
	reader, err := client.ReadRaw(opCtx, filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()

	// Probe for binary content.
	probe := make([]byte, 8*1024)
	probeN, probeErr := reader.Read(probe)
	if probeErr != nil && probeErr != io.EOF {
		return nil, fmt.Errorf("read probe: %w", probeErr)
	}
	// PDF before the generic binary probe: a PDF may carry no NUL byte in its
	// first 8KB and would otherwise be read out as garbled text.
	if bytes.HasPrefix(probe[:probeN], []byte("%PDF-")) || strings.HasSuffix(strings.ToLower(filePath), ".pdf") {
		if !session.SupportsFileInput {
			return nil, errors.New("file is a PDF document and the current model has no native PDF input (file-input capability). Ask the user to switch to a PDF-capable model; do not attempt to install PDF readers")
		}
		return ReadImageFromContainer(opCtx, client, filePath, defaultReadMediaMaxBytes), nil
	}
	if bytes.IndexByte(probe[:probeN], 0) >= 0 {
		if !session.SupportsImageInput {
			return nil, errors.New("file appears to be binary. Read tool only supports text files (image reading not available for this model)")
		}
		return ReadImageFromContainer(opCtx, client, filePath, defaultReadMediaMaxBytes), nil
	}

	// Read remaining content after probe.
	var buf strings.Builder
	buf.Write(probe[:probeN])
	if probeErr != io.EOF {
		remaining, readErr := io.ReadAll(reader)
		if readErr != nil {
			return nil, fmt.Errorf("read file: %w", readErr)
		}
		buf.Write(remaining)
	}

	fullContent := buf.String()
	lines := strings.Split(fullContent, "\n")
	totalLines := len(lines)

	// Apply line_offset and n_lines.
	start := lineOffset - 1 // convert to 0-based
	if start > totalLines {
		start = totalLines
	}
	end := totalLines
	if nLines > 0 && start+nLines < end {
		end = start + nLines
	}

	selectedLines := lines[start:end]
	content := strings.Join(selectedLines, "\n")
	if !strings.HasSuffix(content, "\n") && end < totalLines {
		content += "\n"
	}

	content = addLineNumbers(content, lineOffset)
	return map[string]any{"content": content, "total_lines": totalLines}, nil
}

func (p *ContainerProvider) execWrite(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	opCtx, opCancel := context.WithTimeout(ctx, containerOpTimeout)
	defer opCancel()

	target, err := p.resolveToolTarget(opCtx, session, args)
	if err != nil {
		return nil, err
	}
	client := target.client
	filePath := target.workspace.resolveToolPath(StringArg(args, "path"))
	content := StringArg(args, "content")
	if filePath == "" {
		return nil, errors.New("path is required")
	}

	data := []byte(content)
	if res, err := p.runWorkspaceToolHook(ctx, session, target.hookWorkspaceInfo(p.execWorkDir), hooks.EventBeforeFileWrite, map[string]any{
		"path":  filePath,
		"bytes": len(data),
	}); err != nil {
		if res.Decision == hooks.DecisionDeny {
			return nil, err
		}
		return nil, fmt.Errorf("before file write hook failed: %w", err)
	}
	if len(data) > largeFileThreshold {
		// Large content: use streaming WriteRaw to avoid loading everything
		// into a single gRPC message and to allow incremental transfer.
		if _, err := client.WriteRaw(opCtx, filePath, strings.NewReader(content)); err != nil {
			return nil, err
		}
	} else {
		// Small content: simple synchronous write.
		if err := client.WriteFile(opCtx, filePath, data); err != nil {
			return nil, err
		}
	}
	if _, err := p.runWorkspaceToolHook(context.WithoutCancel(ctx), session, target.hookWorkspaceInfo(p.execWorkDir), hooks.EventAfterFileWrite, map[string]any{
		"path":  filePath,
		"bytes": len(data),
	}); err != nil {
		p.logWorkspaceToolHookError(hooks.EventAfterFileWrite, session.BotID, session.SessionID, err)
	}
	return map[string]any{"ok": true}, nil
}

func (p *ContainerProvider) execList(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	opCtx, opCancel := context.WithTimeout(ctx, containerOpTimeout)
	defer opCancel()

	target, err := p.resolveToolTarget(opCtx, session, args)
	if err != nil {
		return nil, err
	}
	client := target.client
	dirPath := target.workspace.resolveToolPath(StringArg(args, "path"))
	if dirPath == "" {
		dirPath = "."
	}
	recursive, _, _ := BoolArg(args, "recursive")

	offset := int32(0)
	if v, ok, err := IntArg(args, "offset"); err != nil {
		return nil, fmt.Errorf("invalid offset: %w", err)
	} else if ok {
		if v < 0 {
			return nil, errors.New("offset must be >= 0")
		}
		if v > math.MaxInt32 {
			return nil, errors.New("offset exceeds maximum")
		}
		offset = int32(v) //nolint:gosec // bounded above
	}

	limit := int32(listMaxEntries)
	if v, ok, err := IntArg(args, "limit"); err != nil {
		return nil, fmt.Errorf("invalid limit: %w", err)
	} else if ok {
		if v < 1 {
			return nil, errors.New("limit must be >= 1")
		}
		if v > listMaxEntries {
			v = listMaxEntries
		}
		limit = int32(v) //nolint:gosec // bounded by listMaxEntries
	}

	var collapseThreshold int32
	if recursive {
		collapseThreshold = listCollapseThreshold
	}

	result, err := client.ListDir(opCtx, dirPath, recursive, offset, limit, collapseThreshold)
	if err != nil {
		return nil, err
	}

	entriesMaps := make([]map[string]any, 0, len(result.Entries))
	for _, e := range result.Entries {
		m := map[string]any{
			"path": e.GetPath(), "is_dir": e.GetIsDir(), "size": e.GetSize(),
			"mode": e.GetMode(), "mod_time": e.GetModTime(),
		}
		if s := e.GetSummary(); s != "" {
			m["summary"] = s
		}
		entriesMaps = append(entriesMaps, m)
	}

	return map[string]any{
		"path":        dirPath,
		"entries":     entriesMaps,
		"total_count": result.TotalCount,
		"truncated":   result.Truncated,
		"offset":      offset,
		"limit":       limit,
	}, nil
}

func (p *ContainerProvider) execEdit(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	opCtx, opCancel := context.WithTimeout(ctx, containerOpTimeout)
	defer opCancel()

	target, err := p.resolveToolTarget(opCtx, session, args)
	if err != nil {
		return nil, err
	}
	client := target.client
	filePath := target.workspace.resolveToolPath(StringArg(args, "path"))
	oldText := StringArg(args, "old_text")
	newText := StringArg(args, "new_text")
	if filePath == "" || oldText == "" {
		return nil, errors.New("path, old_text and new_text are required")
	}

	// Read file content via streaming RPC.
	reader, err := client.ReadRaw(opCtx, filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	updated, err := applyEdit(string(raw), filePath, oldText, newText)
	if err != nil {
		return nil, err
	}

	updatedBytes := []byte(updated)
	if res, err := p.runWorkspaceToolHook(ctx, session, target.hookWorkspaceInfo(p.execWorkDir), hooks.EventBeforeFileWrite, map[string]any{
		"path":  filePath,
		"bytes": len(updatedBytes),
		"mode":  "edit",
	}); err != nil {
		if res.Decision == hooks.DecisionDeny {
			return nil, err
		}
		return nil, fmt.Errorf("before file write hook failed: %w", err)
	}
	if len(updatedBytes) > largeFileThreshold {
		// Large result: stream-write to avoid gRPC message size issues.
		if _, err := client.WriteRaw(opCtx, filePath, strings.NewReader(updated)); err != nil {
			return nil, err
		}
	} else {
		if err := client.WriteFile(opCtx, filePath, updatedBytes); err != nil {
			return nil, err
		}
	}
	if _, err := p.runWorkspaceToolHook(context.WithoutCancel(ctx), session, target.hookWorkspaceInfo(p.execWorkDir), hooks.EventAfterFileWrite, map[string]any{
		"path":  filePath,
		"bytes": len(updatedBytes),
		"mode":  "edit",
	}); err != nil {
		p.logWorkspaceToolHookError(hooks.EventAfterFileWrite, session.BotID, session.SessionID, err)
	}
	return map[string]any{"ok": true}, nil
}

func (p *ContainerProvider) execExec(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	target, err := p.resolveToolTarget(ctx, session, args)
	if err != nil {
		return nil, err
	}
	client := target.client
	command := strings.TrimSpace(StringArg(args, "command"))
	if command == "" {
		return nil, errors.New("command is required")
	}
	workDir := strings.TrimSpace(StringArg(args, "work_dir"))
	switch {
	case workDir != "":
		// A relative work_dir resolves against the working directory like
		// every other tool path.
		workDir = target.workspace.resolveToolPath(workDir)
	case target.workspace.workdirPath != "":
		workDir = target.workspace.workdirPath
	default:
		workDir = target.workspace.defaultWorkDir
	}
	description := strings.TrimSpace(StringArg(args, "description"))
	hookWorkspace := target.hookWorkspaceInfo(p.execWorkDir)
	backgroundOutputDir := target.backgroundOutputDir()

	// Parse timeout (default 30s, max 600s).
	timeout := background.DefaultExecTimeout
	if t, ok, err := IntArg(args, "timeout"); err != nil {
		return nil, fmt.Errorf("invalid timeout: %w", err)
	} else if ok {
		if t < 1 {
			return nil, errors.New("timeout must be >= 1")
		}
		maxTimeout := int(background.MaxExecTimeout)
		if t > maxTimeout {
			t = maxTimeout
		}
		timeout = int32(t) //nolint:gosec // bounded above
	}

	// Block sleep N (N>=2) in foreground — nudge model toward run_in_background.
	runInBg, _, _ := BoolArg(args, "run_in_background")
	if !runInBg {
		if reason := detectBlockedSleep(command); reason != "" {
			return nil, fmt.Errorf("blocked: %s. Run blocking commands in the background with run_in_background: true, then use wait_until(task_id) and get_background_status(task_id). If you genuinely need a delay (rate limiting, deliberate pacing), keep it under 2 seconds", reason)
		}
	}

	// Background execution path.
	if runInBg && p.bgManager != nil {
		return p.execWithWorkspaceHooks(ctx, session, hookWorkspace, command, workDir, timeout, true, func() (any, error) {
			return p.execExecBackground(ctx, session, client, command, workDir, description, backgroundOutputDir)
		})
	}

	// If we have a background manager, use streaming exec so we can flip
	// to background on timeout without killing the process.
	if p.bgManager != nil {
		return p.execWithWorkspaceHooks(ctx, session, hookWorkspace, command, workDir, timeout, false, func() (any, error) {
			return p.execExecWithFlip(ctx, session, client, command, workDir, description, backgroundOutputDir, timeout)
		})
	}

	// Fallback: no background manager, plain synchronous exec.
	wrapped, err := p.execWithWorkspaceHooks(ctx, session, hookWorkspace, command, workDir, timeout, false, func() (any, error) {
		result, err := client.Exec(ctx, command, workDir, timeout)
		if err != nil {
			return nil, err
		}
		stdout := pruneToolOutputText(result.Stdout, "tool result (exec stdout)")
		stderr := pruneToolOutputText(result.Stderr, "tool result (exec stderr)")
		return map[string]any{"stdout": stdout, "stderr": stderr, "exit_code": result.ExitCode}, nil
	})
	if err != nil {
		return nil, err
	}
	return wrapped, nil
}

func (p *ContainerProvider) execWithWorkspaceHooks(ctx context.Context, session SessionContext, workspace hooks.WorkspaceInfo, command, workDir string, timeout int32, background bool, run func() (any, error)) (any, error) {
	if res, err := p.runWorkspaceToolHook(ctx, session, workspace, hooks.EventBeforeWorkspaceCommand, map[string]any{
		"command":           command,
		"work_dir":          workDir,
		"timeout_seconds":   timeout,
		"run_in_background": background,
	}); err != nil {
		if res.Decision == hooks.DecisionDeny {
			return nil, err
		}
		return nil, fmt.Errorf("before workspace command hook failed: %w", err)
	}
	result, runErr := run()
	extra := map[string]any{
		"command":           command,
		"work_dir":          workDir,
		"timeout_seconds":   timeout,
		"run_in_background": background,
	}
	if runErr != nil {
		extra["error"] = runErr.Error()
	}
	if _, err := p.runWorkspaceToolHook(context.WithoutCancel(ctx), session, workspace, hooks.EventAfterWorkspaceCommand, extra); err != nil {
		p.logWorkspaceToolHookError(hooks.EventAfterWorkspaceCommand, session.BotID, session.SessionID, err)
	}
	return result, runErr
}

const backgroundReplayBytes = 4096

type backgroundExecStreamReader struct {
	resultCh chan background.AdoptResult
	logger   *slog.Logger
	command  string

	mu             sync.Mutex
	stdout         strings.Builder
	stderr         strings.Builder
	chunkHandler   func(stream, chunk string)
	outputRecorded bool
}

func startBackgroundExecStreamReader(log *slog.Logger, stream *bridge.ExecStream, cancel context.CancelFunc, command string) *backgroundExecStreamReader {
	reader := &backgroundExecStreamReader{
		resultCh: make(chan background.AdoptResult, 1),
		logger:   log,
		command:  command,
	}
	go reader.run(stream, cancel)
	return reader
}

func (r *backgroundExecStreamReader) run(stream *bridge.ExecStream, cancel context.CancelFunc) {
	defer cancel()
	var exitCode int32
	var exitReceived bool
	for {
		msg, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			r.logger.Warn("background exec stream recv error",
				slog.String("command", truncateStr(r.command, 80)),
				slog.Any("error", recvErr),
			)
			stdout, stderr, outputRecorded := r.snapshot()
			r.resultCh <- background.AdoptResult{
				Stdout:         stdout,
				Stderr:         stderr,
				ExitCode:       exitCode,
				ExitReceived:   exitReceived,
				Err:            recvErr,
				OutputRecorded: outputRecorded,
			}
			return
		}
		switch msg.GetStream() {
		case pb.ExecOutput_STDOUT:
			r.appendChunk("stdout", string(msg.GetData()))
		case pb.ExecOutput_STDERR:
			r.appendChunk("stderr", string(msg.GetData()))
		case pb.ExecOutput_EXIT:
			exitCode = msg.GetExitCode()
			exitReceived = true
		}
	}

	stdout, stderr, outputRecorded := r.snapshot()
	r.resultCh <- background.AdoptResult{
		Stdout:         stdout,
		Stderr:         stderr,
		ExitCode:       exitCode,
		ExitReceived:   exitReceived,
		OutputRecorded: outputRecorded,
	}
}

func (r *backgroundExecStreamReader) snapshot() (stdout, stderr string, outputRecorded bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stdout.String(), r.stderr.String(), r.outputRecorded
}

func (r *backgroundExecStreamReader) appendChunk(stream, chunk string) {
	if chunk == "" {
		return
	}
	r.mu.Lock()
	switch stream {
	case "stderr":
		r.stderr.WriteString(chunk)
	default:
		r.stdout.WriteString(chunk)
	}
	handler := r.chunkHandler
	r.mu.Unlock()
	if handler != nil {
		handler(stream, chunk)
	}
}

func (r *backgroundExecStreamReader) SetChunkHandler(handler func(stream, chunk string)) {
	r.mu.Lock()
	r.chunkHandler = handler
	r.outputRecorded = handler != nil
	stdout := tailText(r.stdout.String(), backgroundReplayBytes)
	stderr := tailText(r.stderr.String(), backgroundReplayBytes)
	r.mu.Unlock()

	if handler == nil {
		return
	}
	if stdout != "" {
		handler("stdout", stdout)
	}
	if stderr != "" {
		handler("stderr", stderr)
	}
}

func (r *backgroundExecStreamReader) Result() <-chan background.AdoptResult {
	return r.resultCh
}

func tailText(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return value[len(value)-maxBytes:]
}

// execExecWithFlip runs a command via ExecStream with a client-side soft timeout.
// If the command finishes within the timeout, it returns the result normally.
// If the soft timeout fires first, the running stream is handed off to the
// background manager — the process keeps running in the container, and the
// agent gets an immediate "auto_backgrounded" response.
func (p *ContainerProvider) execExecWithFlip(
	ctx context.Context, session SessionContext, client *bridge.Client,
	command, workDir, description, outputDir string, softTimeout int32,
) (any, error) {
	// Start streaming exec with a large container-side timeout so the process
	// keeps running even after we stop reading in the foreground.
	// Use a fully independent context (not derived from the agent request ctx)
	// so the gRPC stream is never cancelled when the foreground session ends.
	streamCtx, streamCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(background.BackgroundExecTimeout)*time.Second)
	stream, err := client.ExecStream(streamCtx, command, workDir, background.BackgroundExecTimeout)
	if err != nil {
		streamCancel()
		return nil, err
	}
	reader := startBackgroundExecStreamReader(p.logger, stream, streamCancel, command)

	// Wait for either the result or soft timeout.
	timer := time.NewTimer(time.Duration(softTimeout) * time.Second)
	defer timer.Stop()

	select {
	case r := <-reader.Result():
		// Command finished within the soft timeout — return normally.
		if r.Err != nil {
			return nil, r.Err
		}
		stdout := pruneToolOutputText(r.Stdout, "tool result (exec stdout)")
		stderr := pruneToolOutputText(r.Stderr, "tool result (exec stderr)")
		return map[string]any{"stdout": stdout, "stderr": stderr, "exit_code": r.ExitCode}, nil

	case <-timer.C:
		// Soft timeout fired — flip the running stream to background.
		// The container process is still alive; we hand off the stream reader
		// goroutine to the background manager.
		return p.flipToBackground(ctx, session, client, reader, command, workDir, description, outputDir, softTimeout)

	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// flipToBackground registers the already-running stream as a background task.
// The goroutine reading from the stream continues; its result feeds the task.
func (p *ContainerProvider) flipToBackground(
	ctx context.Context,
	session SessionContext, client *bridge.Client,
	reader *backgroundExecStreamReader,
	command, workDir, description, outputDir string, softTimeout int32,
) (any, error) {
	writeFn := func(ctx context.Context, path string, data []byte) error {
		return client.WriteFile(ctx, path, data)
	}

	taskID, outputFile := p.bgManager.SpawnAdopt(
		ctx,
		session.BotID, session.SessionID, command, workDir, description, outputDir,
		reader.Result(), writeFn,
	)
	// SetChunkHandler replays output collected during the foreground phase
	// into the task buffer, so the tail below already contains it.
	reader.SetChunkHandler(func(stream, chunk string) {
		p.bgManager.RecordOutput(taskID, stream, chunk)
	})

	p.logger.Info("foreground exec flipped to background",
		slog.String("task_id", taskID),
		slog.String("command", truncateStr(command, 120)),
		slog.Int("soft_timeout_seconds", int(softTimeout)),
	)

	result := map[string]any{
		"status":      "auto_backgrounded",
		"task_id":     taskID,
		"output_file": outputFile,
		"message": fmt.Sprintf(
			"Command exceeded the foreground timeout (%ds) and has been moved to the background with task ID: %s. "+
				"The process is still running — no work was lost; output collected so far is in output_tail. "+
				"Use wait_until(task_id) to keep observing: it returns when the task finishes, stalls, or goes quiet (reason 'idle') — for servers, a ready message in output_tail means it is up. "+
				"The full log is written to %s after the task ends. "+
				"For long-running commands, use run_in_background: true from the start to avoid this delay.",
			softTimeout, taskID, outputFile,
		),
	}
	if task := p.bgManager.Get(taskID); task != nil {
		if tail := task.OutputTail(); tail != "" {
			result["output_tail"] = tail
		}
	}
	return result, nil
}

// detectBlockedSleep checks if the command starts with `sleep N` where N >= 2.
// Returns a human-readable reason string, or "" if the command is allowed.
func detectBlockedSleep(command string) string {
	cmd := strings.TrimSpace(command)
	m := blockedSleepPattern.FindStringSubmatch(cmd)
	if m == nil {
		return ""
	}
	seconds, err := strconv.ParseFloat(m[1], 64)
	if err != nil || seconds < 2 {
		return ""
	}
	return fmt.Sprintf("sleep %.0f is not allowed in foreground execution", seconds)
}

// execExecBackground spawns the command as a background task and returns immediately.
func (p *ContainerProvider) execExecBackground(
	ctx context.Context, session SessionContext, client *bridge.Client,
	command, workDir, description, outputDir string,
) (any, error) {
	streamCtx, streamCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(background.BackgroundExecTimeout)*time.Second)
	stream, err := client.ExecStream(streamCtx, command, workDir, background.BackgroundExecTimeout)
	if err != nil {
		streamCancel()
		return nil, err
	}
	reader := startBackgroundExecStreamReader(p.logger, stream, streamCancel, command)

	writeFn := func(ctx context.Context, path string, data []byte) error {
		return client.WriteFile(ctx, path, data)
	}
	taskID, outputFile := p.bgManager.SpawnAdopt(
		ctx,
		session.BotID, session.SessionID, command, workDir, description, outputDir,
		reader.Result(), writeFn,
	)
	reader.SetChunkHandler(func(stream, chunk string) {
		p.bgManager.RecordOutput(taskID, stream, chunk)
	})

	return map[string]any{
		"status":      "background_started",
		"task_id":     taskID,
		"output_file": outputFile,
		"message": fmt.Sprintf(
			"Command started in background with task ID: %s. "+
				"Use wait_until(task_id) to observe it: it returns when the task finishes, stalls on a prompt, or output goes quiet (reason 'idle'), always with the latest output_tail — for servers, a ready message there means it is up. "+
				"get_background_status(task_id) also shows output_tail while running. The full log is written to %s after the task ends.",
			taskID, outputFile,
		),
	}, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func addLineNumbers(content string, startLine int) string {
	if content == "" {
		return content
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	var out strings.Builder
	out.Grow(len(content) + len(lines)*8)
	for i, line := range lines {
		fmt.Fprintf(&out, "%6d\t%s\n", startLine+i, line)
	}
	return out.String()
}
