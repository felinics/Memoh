package client

import (
	"context"
	"errors"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/toolcontext"
)

// createElicitationRequest accepts the v0.13.5 form shape plus the optional
// session/tool-call extensions emitted by some adapters. The protocol form
// itself carries neither field, so ownership comes from the callback's current
// Memoh tool session and the attached ACP Session.
type createElicitationRequest struct {
	Meta            map[string]any `json:"_meta,omitempty"`
	SessionID       string         `json:"sessionId,omitempty"`
	ToolCallID      string         `json:"toolCallId,omitempty"`
	Mode            string         `json:"mode"`
	Message         string         `json:"message"`
	RequestedSchema map[string]any `json:"requestedSchema"`
}

func (r *createElicitationRequest) Validate() error {
	if r == nil {
		return errors.New("elicitation request is required")
	}
	if r.Mode != "form" {
		return errors.New("only form elicitation is supported")
	}
	if strings.TrimSpace(r.Message) == "" {
		return errors.New("elicitation message is required")
	}
	if r.RequestedSchema == nil {
		return errors.New("elicitation requestedSchema is required")
	}
	return nil
}

// CreateElicitation handles structured input and MCP form elicitation. Codex
// marks MCP tool-call consent in _meta; that shape is permission, not data, and
// is routed through the same approval policy/audit path as RequestPermission.
func (c *clientCallbacks) CreateElicitation(ctx context.Context, request createElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	if c == nil {
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}
	c.decisions.enter()
	defer c.decisions.exit()
	session := c.currentToolSession()
	if strings.TrimSpace(session.BotID) == "" || strings.TrimSpace(session.SessionID) == "" {
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}
	acpSessionID := c.elicitationACPSessionID(request.SessionID)
	if c.userInput == nil || !session.CanRequestUserInput {
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}

	input, mapping, err := userinput.ElicitationFormInput(request.Message, request.RequestedSchema)
	if err != nil {
		return acp.UnstableCreateElicitationResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}

	toolCallID := request.ToolCallID
	if strings.TrimSpace(toolCallID) == "" {
		toolCallID = "acp-elicitation-" + uuid.NewString()
	}
	expiresAt := time.Now().Add(userinput.DefaultWaitTimeout + time.Minute)

	// The durable request and stream projection survive browser refreshes and
	// reconnects. The blocked JSON-RPC callback and its field mapping are
	// process-local; cancellation, timeout, or runtime teardown invalidates the
	// waiter rather than pretending the form can resume after a process restart.
	ctx, cancel := toolcontext.Bind(ctx, session)
	defer cancel()
	providerMetadata := map[string]any{
		"source":     userinput.ProviderSourceACPElicitation,
		"runtime_id": session.RuntimeID,
		"run_id":     session.RunID,
	}
	if acpSessionID != "" {
		providerMetadata["acp_session_id"] = string(acpSessionID)
	}
	flow, err := userinput.RunFlow(ctx, c.userInput, userinput.FlowRequest{
		Input: userinput.CreatePendingInput{
			BotID:                        session.BotID,
			SessionID:                    session.SessionID,
			RouteID:                      session.RouteID,
			ChannelIdentityID:            session.ChannelIdentityID,
			RequestedByChannelIdentityID: session.ChannelIdentityID,
			ToolCallID:                   toolCallID,
			ToolName:                     userinput.ToolNameAskUser,
			Input:                        input,
			ProviderMetadata:             providerMetadata,
			SourcePlatform:               session.CurrentPlatform,
			ReplyTarget:                  session.ReplyTarget,
			ConversationType:             session.ConversationType,
			ExpiresAt:                    &expiresAt,
		},
		ActorChannelIdentityID: session.ChannelIdentityID,
		Interactive:            true,
		WaitTimeout:            userinput.DefaultWaitTimeout,
		UndeliveredReason:      "elicitation could not be delivered to the interactive stream",
		TimeoutReason:          "elicitation timed out",
		AbortReason:            "elicitation aborted",
		Emit:                   c.emitElicitationUserInput,
	})
	if err != nil {
		return acp.UnstableCreateElicitationResponse{}, err
	}

	switch flow.Request.Status {
	case userinput.StatusSubmitted:
		content, err := mapping.Content(flow.Request)
		if err != nil {
			return acp.UnstableCreateElicitationResponse{}, err
		}
		return acp.UnstableCreateElicitationResponse{
			Accept: &acp.UnstableCreateElicitationAccept{Action: "accept", Content: content},
		}, nil
	case userinput.StatusCanceled:
		if strings.TrimSpace(stringFromAny(flow.Request.Result["reason"])) == "user_canceled" {
			return acp.NewUnstableCreateElicitationResponseDecline(), nil
		}
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	default:
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}
}

func (c *clientCallbacks) elicitationACPSessionID(extension string) acp.SessionId {
	if sessionID := strings.TrimSpace(extension); sessionID != "" {
		return acp.SessionId(sessionID)
	}
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.runtimeSession == nil {
		return ""
	}
	return c.runtimeSession.sessionID
}

func (c *clientCallbacks) emitElicitationUserInput(req userinput.Request) bool {
	if c == nil || c.events == nil {
		return false
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = userinput.StatusPending
	}
	ev := event.StreamEvent{
		Type:        event.UserInputRequest,
		ToolCallID:  req.ToolCallID,
		ToolName:    req.ToolName,
		Input:       req.Input,
		UserInputID: req.ID,
		ShortID:     req.ShortID,
		Status:      status,
		Metadata:    userinput.DeferredMetadata(req),
	}
	if !strings.EqualFold(status, userinput.StatusPending) {
		return c.events.emitTerminalDecision(ev)
	}
	return c.events.emit(ev)
}
