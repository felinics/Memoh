package codex

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

var (
	_ external.ModeProvider    = (*Driver)(nil)
	_ external.CommandProvider = (*Driver)(nil)
)

func (*Driver) Commands(_ context.Context, input external.PromptInput) ([]external.Command, error) {
	commands := []external.Command{
		{Name: "review", Description: "Review workspace changes", InputHint: "Optional review instructions", Kind: external.CommandTurn},
		{Name: "status", Description: "Show thread status, token usage and account limits", Kind: external.CommandRead},
		{Name: "skills", Description: "List available skills", Kind: external.CommandRead},
		{Name: "mcp", Description: "List MCP server status", Kind: external.CommandRead},
		{Name: "compact", Description: "Compact thread context", Kind: external.CommandOperation, RunningText: "Compacting", CompletedText: "Compaction completed"},
	}
	if strings.HasPrefix(input.Language, "zh") {
		commands[4].RunningText = "压缩中"
		commands[4].CompletedText = "压缩已完成"
	} else if strings.HasPrefix(input.Language, "ja") {
		commands[4].RunningText = "圧縮中"
		commands[4].CompletedText = "圧縮が完了しました"
	}
	commands = append(commands, external.Command{Name: "goal", Description: "Set a goal to keep pursuing", InputHint: "Describe your goal", Kind: external.CommandTurn})
	return commands, nil
}

func (d *Driver) Modes(ctx context.Context, input external.PromptInput) (external.ModeState, error) {
	agent, err := d.agents.Get(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return external.ModeState{}, err
	}
	mode := firstNonEmpty(metadataString(input.RuntimeMetadata, "permission_mode"), metadataString(agent.Metadata, "permission_mode"), "strict")
	return localizedCodexModes(mode, input.Language)
}

func codexModes(current string) (external.ModeState, error) {
	switch current {
	case "strict", "policy", "yolo":
	default:
		return external.ModeState{}, external.ErrModeUnavailable
	}
	return external.ModeState{Supported: true, CurrentModeID: current, AvailableModes: []external.Mode{
		{ID: "strict", Name: "Ask for approval", Description: "Always ask to edit external files and use the internet", Icon: "hand"},
		{ID: "policy", Name: "Approve for me", Description: "Only ask for actions detected as potentially unsafe", Icon: "shield-terminal"},
		{ID: "yolo", Name: "Full access", Description: "Unrestricted access to the internet and any file in your workspace", Icon: "shield-alert", Warning: true},
	}}, nil
}

// The application persists the choice under the thread admission guard. Each
// new turn supplies it explicitly, including when the Codex thread is warm.
func (*Driver) SetMode(_ context.Context, input external.PromptInput, mode string) (external.ModeState, error) {
	return localizedCodexModes(mode, input.Language)
}

// English copy follows the supplied Codex app screenshot, with computer
// replaced by workspace. Other locales preserve the same approval semantics.
func localizedCodexModes(current, language string) (external.ModeState, error) {
	state, err := codexModes(current)
	if err != nil {
		return state, err
	}
	var labels, descriptions []string
	switch {
	case strings.HasPrefix(language, "zh"):
		labels = []string{"请求批准", "帮我批准", "完全访问"}
		descriptions = []string{"编辑外部文件和使用互联网时始终询问", "仅在检测到可能不安全的操作时询问", "不受限制地访问互联网和工作区中的任何文件"}
	case strings.HasPrefix(language, "ja"):
		labels = []string{"承認を求める", "代わりに承認", "フルアクセス"}
		descriptions = []string{"外部ファイルの編集やインターネットの使用時に常に確認する", "安全でない可能性があると検出された操作のみ確認する", "インターネットとワークスペース内のすべてのファイルに制限なくアクセスする"}
	}
	for i := range labels {
		state.AvailableModes[i].Name = labels[i]
		state.AvailableModes[i].Description = descriptions[i]
	}
	return state, nil
}

type permissionPreset struct {
	approval protocol.AskForApproval
	reviewer protocol.ApprovalsReviewer
	sandbox  protocol.SandboxMode
	policy   protocol.SandboxPolicy
}

func permissions(cfg Config, input external.PromptInput) (permissionPreset, error) {
	mode := firstNonEmpty(metadataString(input.RuntimeMetadata, "permission_mode"), cfg.PermissionMode, "strict")
	if _, err := codexModes(mode); err != nil {
		return permissionPreset{}, err
	}
	p := permissionPreset{
		approval: protocol.AskForApproval{Unit: protocol.AskForApprovalUnitOnRequest},
		reviewer: protocol.ApprovalsReviewerUser,
		sandbox:  protocol.SandboxModeWorkspaceWrite,
		policy:   protocol.SandboxPolicy{WorkspaceWrite: &protocol.WorkspaceWriteSandboxPolicy{}},
	}
	switch mode {
	case "policy":
		p.reviewer = protocol.ApprovalsReviewerAutoReview
	case "yolo":
		p.approval = protocol.AskForApproval{Unit: protocol.AskForApprovalUnitNever}
		p.sandbox = protocol.SandboxModeDangerFullAccess
		p.policy = protocol.SandboxPolicy{DangerFullAccess: &protocol.DangerFullAccessSandboxPolicy{}}
	}
	return p, nil
}

func (d *Driver) ReadCommand(ctx context.Context, input external.PromptInput) (string, error) {
	commands, _ := d.Commands(ctx, input)
	command, ok := external.FindCommand(commands, input.Command)
	if !ok || command.Kind != external.CommandRead {
		return "", external.ErrCommandUnavailable
	}
	if input.Command == "status" {
		return d.cachedStatus(input), nil
	}
	// Reads must not enter account/login/start. Credentials are checked before
	// acquisition; account/read below only observes the materialized login.
	if _, _, err := d.resolveAgentConfig(ctx, input.BotID, input.BotAgentID, true); err != nil {
		return "", err
	}
	srv, release, err := d.acquireServer(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return "", err
	}
	defer release()
	var account protocol.GetAccountResponse
	if err := srv.conn.Call(ctx, protocol.MethodAccountRead, protocol.GetAccountParams{}, &account); err != nil {
		return "", err
	}
	if account.Account == nil {
		return "", ErrAuthRequired
	}
	var lines []string
	switch input.Command {
	case "skills":
		cwd := firstNonEmpty(metadataString(input.RuntimeMetadata, "project_path"), defaultProjectPath)
		var response protocol.SkillsListResponse
		if err := srv.conn.Call(ctx, protocol.MethodSkillsList, protocol.SkillsListParams{Cwds: []string{cwd}}, &response); err != nil {
			return "", err
		}
		for _, entry := range response.Data {
			for _, skill := range entry.Skills {
				lines = append(lines, fmt.Sprintf("- %s: %s", skill.Name, skill.Description))
			}
		}
	case "mcp":
		params := protocol.ListMcpServerStatusParams{}
		if id := metadataString(input.RuntimeMetadata, metadataThreadIDKey); id != "" {
			params.ThreadID = &id
		}
		seen := map[string]bool{}
		for {
			var response protocol.ListMcpServerStatusResponse
			if err := srv.conn.Call(ctx, protocol.MethodMCPServerStatusList, params, &response); err != nil {
				return "", err
			}
			for _, server := range response.Data {
				lines = append(lines, fmt.Sprintf("- %s: %s", server.Name, server.AuthStatus))
			}
			if response.NextCursor == nil || *response.NextCursor == "" {
				break
			}
			if seen[*response.NextCursor] {
				return "", errors.New("codex MCP list repeated cursor")
			}
			seen[*response.NextCursor] = true
			params.Cursor = response.NextCursor
		}
	}
	if len(lines) == 0 {
		return "No entries available.", nil
	}
	return strings.Join(lines, "\n"), nil
}

func (d *Driver) cachedStatus(input external.PromptInput) string {
	threadID := metadataString(input.RuntimeMetadata, metadataThreadIDKey)
	state := "not loaded"
	tokens := input.RuntimeMetadata["codex_thread_total_tokens"]
	window := input.RuntimeMetadata["codex_context_window"]
	limits := "unavailable"
	if d.servers != nil {
		if resource := d.servers.peek(serverKey(input.BotID, input.BotAgentID)); resource != nil {
			srv := resource.(*appServer)
			srv.mu.Lock()
			if status, ok := srv.threadStatus[threadID]; ok {
				state = status.Tag
			}
			if usage, ok := srv.threadUsage[threadID]; ok {
				tokens = usage.Total.TotalTokens
				window = usage.ModelContextWindow
			}
			if srv.rateLimits != nil {
				limits = formatRateLimits(*srv.rateLimits)
			}
			srv.mu.Unlock()
		}
	}
	if threadID == "" {
		state = "not started"
	}
	return fmt.Sprintf("Thread: %s\nStatus: %s\nTokens: %v\nContext window: %v\nLimits: %s", threadID, state, displayValue(tokens), displayValue(window), limits)
}

func displayValue(value any) any {
	if value == nil {
		return "unavailable"
	}
	if number, ok := value.(*int64); ok {
		if number == nil {
			return "unavailable"
		}
		return *number
	}
	return value
}

func formatRateLimits(limits protocol.RateLimitSnapshot) string {
	var values []string
	if limits.Primary != nil {
		values = append(values, fmt.Sprintf("primary %d%% used", limits.Primary.UsedPercent))
	}
	if limits.Secondary != nil {
		values = append(values, fmt.Sprintf("secondary %d%% used", limits.Secondary.UsedPercent))
	}
	if len(values) == 0 {
		return "unavailable"
	}
	return strings.Join(values, ", ")
}

func (s *appServer) cacheControlNotification(decoded any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch value := decoded.(type) {
	case *protocol.ThreadStartedNotification:
		if s.threadStatus == nil {
			s.threadStatus = map[string]protocol.ThreadStatus{}
		}
		s.threadStatus[value.Thread.ID] = value.Thread.Status
	case *protocol.ThreadStatusChangedNotification:
		if s.threadStatus == nil {
			s.threadStatus = map[string]protocol.ThreadStatus{}
		}
		s.threadStatus[value.ThreadID] = value.Status
	case *protocol.ThreadTokenUsageUpdatedNotification:
		if s.threadUsage == nil {
			s.threadUsage = map[string]protocol.ThreadTokenUsage{}
		}
		s.threadUsage[value.ThreadID] = value.TokenUsage
	case *protocol.AccountRateLimitsUpdatedNotification:
		snapshot := value.RateLimits
		s.rateLimits = &snapshot
	}
}

func startRuntimeTurn(ctx context.Context, srv *appServer, input external.PromptInput, params protocol.TurnStartParams, response *protocol.TurnStartResponse) error {
	if input.Command == "goal" {
		return startGoalTurn(ctx, srv, input, params, response)
	}
	if input.Command == "" {
		return srv.conn.Call(ctx, protocol.MethodTurnStart, params, response)
	}
	if input.Command != "review" {
		return external.ErrCommandUnavailable
	}
	// review/start inherits thread settings. Resuming an already-loaded thread
	// ignores overrides, so use the dedicated update without replacing its tools.
	if err := srv.conn.Call(ctx, protocol.MethodThreadSettingsUpdate, protocol.ThreadSettingsUpdateParams{
		ThreadID:          params.ThreadID,
		Model:             params.Model,
		Effort:            params.Effort,
		ApprovalPolicy:    params.ApprovalPolicy,
		ApprovalsReviewer: params.ApprovalsReviewer,
		SandboxPolicy:     params.SandboxPolicy,
		CollaborationMode: params.CollaborationMode,
	}, nil); err != nil {
		return err
	}
	target := protocol.ReviewTarget{UncommittedChanges: &protocol.UncommittedChangesReviewTarget{}}
	instructions := strings.TrimSpace(input.CommandArgs)
	if instructions != "" {
		target = protocol.ReviewTarget{Custom: &protocol.CustomReviewTarget{Instructions: instructions}}
	}
	delivery := protocol.ReviewDeliveryInline
	var review protocol.ReviewStartResponse
	err := srv.conn.Call(ctx, protocol.MethodReviewStart, protocol.ReviewStartParams{ThreadID: params.ThreadID, Delivery: &delivery, Target: target}, &review)
	response.Turn = review.Turn
	return err
}
