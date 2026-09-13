package codex

import (
	"context"
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

var (
	_ external.ModeProvider    = (*Driver)(nil)
	_ external.CommandProvider = (*Driver)(nil)
)

func (*Driver) Commands(_ context.Context, _ external.PromptInput) ([]external.Command, error) {
	return []external.Command{
		{Name: "review", I18nKey: "runtime.codex.commands.review", Kind: external.CommandTurn},
		{Name: "status", I18nKey: "runtime.codex.commands.status", Kind: external.CommandRead},
		{Name: "skills", I18nKey: "runtime.codex.commands.skills", Kind: external.CommandRead},
		{Name: "mcp", I18nKey: "runtime.codex.commands.mcp", Kind: external.CommandRead},
		{Name: "compact", I18nKey: "runtime.codex.commands.compact", Kind: external.CommandOperation},
		{Name: "goal", I18nKey: "runtime.codex.commands.goal", Kind: external.CommandTurn},
	}, nil
}

func (d *Driver) Modes(ctx context.Context, input external.PromptInput) (external.ModeState, error) {
	agent, err := d.agents.Get(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return external.ModeState{}, err
	}
	mode := firstNonEmpty(metadataString(input.RuntimeMetadata, "permission_mode"), metadataString(agent.Metadata, "permission_mode"), "strict")
	return codexModes(mode)
}

func codexModes(current string) (external.ModeState, error) {
	switch current {
	case "strict", "policy", "yolo":
	default:
		return external.ModeState{}, external.ErrModeUnavailable
	}
	return external.ModeState{Kind: "permission", ApplyOnNextTurn: true, Supported: true, CurrentModeID: current, AvailableModes: []external.Mode{
		{ID: "strict", I18nKey: "runtime.codex.modes.strict", Icon: "hand"},
		{ID: "policy", I18nKey: "runtime.codex.modes.policy", Icon: "shield-terminal"},
		{ID: "yolo", I18nKey: "runtime.codex.modes.yolo", Icon: "shield-alert", Warning: true},
	}}, nil
}

// The application persists the choice under the thread admission guard. Each
// new turn supplies it explicitly, including when the Codex thread is warm.
func (*Driver) SetMode(_ context.Context, _ external.PromptInput, mode string) (external.ModeState, error) {
	return codexModes(mode)
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

func (d *Driver) ReadCommand(ctx context.Context, input external.PromptInput) (external.CommandResult, error) {
	switch input.Command {
	case "status":
		return external.CommandResult{Data: d.cachedStatus(input), Notice: "last_observed"}, nil
	case "skills", "mcp":
	default:
		return external.CommandResult{}, external.ErrCommandUnavailable
	}
	// Reads must not enter account/login/start. Credentials are checked before
	// acquisition; account/read below only observes the materialized login.
	if _, _, err := d.resolveAgentConfig(ctx, input.BotID, input.BotAgentID, true); err != nil {
		return external.CommandResult{}, err
	}
	srv, release, err := d.acquireServer(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return external.CommandResult{}, err
	}
	defer release()
	var account protocol.GetAccountResponse
	if err := srv.conn.Call(ctx, protocol.MethodAccountRead, protocol.GetAccountParams{}, &account); err != nil {
		return external.CommandResult{}, err
	}
	if account.Account == nil {
		return external.CommandResult{}, ErrAuthRequired
	}
	entries := []any{}
	switch input.Command {
	case "skills":
		cwd := firstNonEmpty(metadataString(input.RuntimeMetadata, "project_path"), defaultProjectPath)
		var response protocol.SkillsListResponse
		if err := srv.conn.Call(ctx, protocol.MethodSkillsList, protocol.SkillsListParams{Cwds: []string{cwd}}, &response); err != nil {
			return external.CommandResult{}, err
		}
		for _, entry := range response.Data {
			for _, skill := range entry.Skills {
				entries = append(entries, skill)
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
				return external.CommandResult{}, err
			}
			for _, server := range response.Data {
				entries = append(entries, server)
			}
			if response.NextCursor == nil || *response.NextCursor == "" {
				break
			}
			if seen[*response.NextCursor] {
				return external.CommandResult{}, errors.New("codex MCP list repeated cursor")
			}
			seen[*response.NextCursor] = true
			params.Cursor = response.NextCursor
		}
	}
	return external.CommandResult{Data: entries}, nil
}

func (d *Driver) cachedStatus(input external.PromptInput) map[string]any {
	threadID := metadataString(input.RuntimeMetadata, metadataThreadIDKey)
	var state any
	tokens := input.RuntimeMetadata["codex_thread_total_tokens"]
	window := input.RuntimeMetadata["codex_context_window"]
	var limits any
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
				limits = srv.rateLimits
			}
			srv.mu.Unlock()
		}
	}
	return map[string]any{"thread_id": threadID, "status": state, "tokens": tokens, "context_window": window, "limits": limits}
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
