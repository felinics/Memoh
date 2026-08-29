package application

import (
	"context"
	"errors"
	"strings"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
)

var (
	ErrACPModeSessionRequired = errors.New("ACP session is required for permission mode")
	ErrACPModeUnsupported     = errors.New("ACP agent does not expose session modes")
	ErrACPModeUnavailable     = errors.New("ACP session mode is unavailable")
)

type ACPModeRequest struct {
	BotID                  string
	ThreadID               string
	ActorChannelIdentityID string
	ActorUserID            string
	ModeID                 string
	ToolHTTPURL            string
}

type ACPMode struct {
	ID          string
	Name        string
	Description string
}

type ACPModeState struct {
	CurrentModeID string
	Available     []ACPMode
	Changed       bool
}

type acpModeController interface {
	Ensure(context.Context, acpagent.PromptInput) (acpagent.RuntimeStatus, error)
	SetMode(context.Context, acpagent.PromptInput, string) (acpagent.RuntimeStatus, error)
}

// ConfigureACPMode exposes the Agent's session modes without interpreting
// their IDs. An empty ModeID reads state; a non-empty ID is sent unchanged to
// session/set_mode and affects only the live ACP session.
func (s *Service) ConfigureACPMode(ctx context.Context, input ACPModeRequest) (ACPModeState, error) {
	if s == nil || s.sessionService == nil {
		return ACPModeState{}, ErrACPModeSessionRequired
	}
	controller, ok := s.acpPool.(acpModeController)
	if !ok {
		return ACPModeState{}, ErrACPModeUnsupported
	}
	sessionID := strings.TrimSpace(input.ThreadID)
	if sessionID == "" {
		return ACPModeState{}, ErrACPModeSessionRequired
	}
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil || !sessionpkg.IsACPRuntime(sess) {
		return ACPModeState{}, ErrACPModeSessionRequired
	}
	botID := strings.TrimSpace(input.BotID)
	if botID == "" {
		botID = strings.TrimSpace(sess.BotID)
	}
	if botID == "" || strings.TrimSpace(sess.BotID) != botID {
		return ACPModeState{}, toolapproval.ErrForbidden
	}
	if err := s.authorizeACPToolApprovalResponse(ctx, toolapproval.Request{
		BotID:     botID,
		SessionID: sessionID,
		Operation: toolapproval.OperationPermission,
	}, ToolApprovalResponseInput{
		BotID:                  botID,
		ThreadID:               sessionID,
		ActorChannelIdentityID: input.ActorChannelIdentityID,
		ActorUserID:            input.ActorUserID,
	}); err != nil {
		return ACPModeState{}, err
	}

	metadata := mergeACPRuntimeMetadata(sess.Metadata, sess.RuntimeMetadata)
	prompt := acpagent.PromptInput{
		BotID:                 botID,
		SessionID:             sessionID,
		AgentID:               metadataString(metadata, "acp_agent_id"),
		ProjectPath:           metadataString(metadata, "project_path"),
		RuntimeOwnerAccountID: metadataString(metadata, "runtime_owner_account_id"),
		ChannelIdentityID:     strings.TrimSpace(input.ActorChannelIdentityID),
		ToolHTTPURL:           strings.TrimSpace(input.ToolHTTPURL),
	}
	status, err := controller.Ensure(ctx, prompt)
	if err != nil {
		return ACPModeState{}, err
	}
	previous := ""
	if status.Modes != nil {
		previous = status.Modes.CurrentModeID
	}
	modeID := input.ModeID
	if strings.TrimSpace(modeID) != "" {
		status, err = controller.SetMode(ctx, prompt, modeID)
		if err != nil {
			switch {
			case errors.Is(err, acpclient.ErrModeSelectionUnsupported):
				return ACPModeState{}, ErrACPModeUnsupported
			case errors.Is(err, acpclient.ErrModeUnavailable), errors.Is(err, acpclient.ErrModeIDRequired):
				return ACPModeState{}, ErrACPModeUnavailable
			default:
				return ACPModeState{}, err
			}
		}
	}
	if status.Modes == nil || !status.Modes.Supported {
		return ACPModeState{}, ErrACPModeUnsupported
	}
	out := ACPModeState{
		CurrentModeID: status.Modes.CurrentModeID,
		Available:     make([]ACPMode, 0, len(status.Modes.Available)),
	}
	for _, mode := range status.Modes.Available {
		out.Available = append(out.Available, ACPMode{
			ID:          mode.ID,
			Name:        mode.Name,
			Description: mode.Description,
		})
	}
	out.Changed = strings.TrimSpace(modeID) != "" && previous != out.CurrentModeID
	return out, nil
}
