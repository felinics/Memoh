// Bridges codex MCP server elicitation to Memoh's ask_user decision flow.
// Form-mode schemas reuse the shared elicitation core (the same mapping the
// ACP runtime uses); url mode renders as a single confirm card. The protocol
// carries no thread id, so the app-server routes an elicitation to the bot's
// sole active turn and declines when ownership is ambiguous.
package codex

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
)

// dispatchElicitation answers one MCP elicitation. A Memoh tool-call consent
// is decided at the app-server level — it needs only the bot-scoped gateway
// lookup, so concurrent turns cannot break it. Everything that needs a user
// (forms, third-party consents) routes to the bot's sole active turn; with
// zero or several candidates the owner is unknowable and declining is the
// only safe answer.
func (s *appServer) dispatchElicitation(req *protocol.Inbound, params *protocol.McpServerElicitationRequestParams) {
	if toolName, ok := autoAcceptedConsentTool(s.mountCtx, params, s.toolLookup); ok {
		s.logger.Debug("codex: auto-accepting Memoh MCP tool consent; the gateway enforces the real policy",
			slog.String("tool", toolName))
		_ = s.conn.Respond(req.ID, protocol.McpServerElicitationRequestResponse{
			Action: protocol.McpServerElicitationActionAccept,
		})
		return
	}
	turn := s.soleActiveTurn()
	if turn == nil {
		s.logger.Warn("codex: declining MCP elicitation without a unique active turn", slog.String("mode", params.Tag))
		_ = s.conn.Respond(req.ID, protocol.McpServerElicitationRequestResponse{
			Action: protocol.McpServerElicitationActionDecline,
		})
		return
	}
	turn.handleElicitation(s.conn, req, params)
}

// autoAcceptedConsentTool reports whether the elicitation is a consent for a
// tool the Memoh gateway itself serves. The gateway enforces the real
// approval policy on the actual call; a codex-side card here would
// double-approve every gateway tool. This trusts the workspace-owned codex
// config not to alias a foreign server as "memoh" — anyone who can edit that
// config already runs arbitrary commands as the agent, so the codex consent
// layer is not a security boundary against them. Everything else fails
// closed to the user-facing path.
func autoAcceptedConsentTool(ctx context.Context, params *protocol.McpServerElicitationRequestParams, lookup func(context.Context, string) bool) (string, bool) {
	message, _, ok := elicitationConsentEnvelope(params)
	if !ok || lookup == nil {
		return "", false
	}
	serverName, toolName, parsed := mcpConsentTarget(message)
	if !parsed || serverName != memohMCPServerName || !lookup(ctx, toolName) {
		return "", false
	}
	return toolName, true
}

// elicitationConsentEnvelope reports whether the elicitation is a codex MCP
// tool-call consent, returning its message and _meta.
func elicitationConsentEnvelope(params *protocol.McpServerElicitationRequestParams) (string, map[string]any, bool) {
	var message string
	var meta map[string]any
	switch params.Tag {
	case protocol.McpServerElicitationRequestParamsTagForm:
		if params.Form == nil {
			return "", nil, false
		}
		message, meta = params.Form.Message, anyToSchemaMap(params.Form.Meta)
	case protocol.McpServerElicitationRequestParamsTagOpenaiForm:
		if params.OpenaiForm == nil {
			return "", nil, false
		}
		message, meta = params.OpenaiForm.Message, anyToSchemaMap(params.OpenaiForm.Meta)
	default:
		return "", nil, false
	}
	if kind, _ := meta["codex_approval_kind"].(string); kind == "mcp_tool_call" {
		return message, meta, true
	}
	return "", nil, false
}

// soleActiveTurn returns the bot's only running turn, or nil when zero or
// several turns are active.
func (s *appServer) soleActiveTurn() *turnState {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sole *turnState
	for _, turn := range s.turns {
		if turn == nil {
			continue
		}
		if sole != nil {
			return nil
		}
		sole = turn
	}
	return sole
}

// handleElicitation runs a user-facing elicitation through this turn. It
// mirrors handleServerRequest's bookkeeping: the decision is bounded by the
// turn context, registered in inflight so serverRequest/resolved can cancel
// it, and refused outright once the turn has closed.
func (t *turnState) handleElicitation(c *conn, req *protocol.Inbound, params *protocol.McpServerElicitationRequestParams) {
	ctx, cancel := context.WithCancel(t.ctx)
	defer cancel()
	key := req.ID.Key()
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		_ = c.Respond(req.ID, protocol.McpServerElicitationRequestResponse{
			Action: protocol.McpServerElicitationActionCancel,
		})
		return
	}
	t.inflight[key] = cancel
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.inflight, key)
		t.mu.Unlock()
	}()
	_ = c.Respond(req.ID, t.runElicitation(ctx, params))
}

func (t *turnState) runElicitation(ctx context.Context, params *protocol.McpServerElicitationRequestParams) protocol.McpServerElicitationRequestResponse {
	decline := protocol.McpServerElicitationRequestResponse{Action: protocol.McpServerElicitationActionDecline}
	cancel := protocol.McpServerElicitationRequestResponse{Action: protocol.McpServerElicitationActionCancel}
	if t == nil || t.userInput == nil || !t.input.CanRequestUserInput {
		return decline
	}

	var message string
	var schema map[string]any
	var meta map[string]any
	switch params.Tag {
	case protocol.McpServerElicitationRequestParamsTagForm:
		if params.Form == nil {
			return decline
		}
		message = params.Form.Message
		schema = anyToSchemaMap(params.Form.RequestedSchema)
		meta = anyToSchemaMap(params.Form.Meta)
	case protocol.McpServerElicitationRequestParamsTagOpenaiForm:
		if params.OpenaiForm == nil {
			return decline
		}
		message = params.OpenaiForm.Message
		schema = anyToSchemaMap(params.OpenaiForm.RequestedSchema)
		meta = anyToSchemaMap(params.OpenaiForm.Meta)
	case protocol.McpServerElicitationRequestParamsTagURL:
		if params.URL == nil {
			return decline
		}
		return t.runURLElicitation(ctx, params.URL)
	default:
		t.logger.Warn("codex: declining MCP elicitation with unsupported mode", slog.String("mode", params.Tag))
		t.emitElicitationDeclinedNotice("the requested interaction format is not supported")
		return decline
	}

	// Codex marks MCP tool-call consent in _meta. That shape is permission,
	// not data: it routes through the approval path, never the form mapper
	// (whose empty-properties schema it would fail anyway).
	if approvalKind, _ := meta["codex_approval_kind"].(string); approvalKind == "mcp_tool_call" {
		return t.runMCPToolConsent(ctx, message, meta)
	}
	if schema == nil {
		t.logger.Warn("codex: declining MCP elicitation with unreadable schema", slog.String("mode", params.Tag))
		t.emitElicitationDeclinedNotice("the requested form cannot be read")
		return decline
	}

	input, mapping, err := userinput.ElicitationFormInput(message, schema)
	if err != nil {
		t.logger.Warn("codex: declining unsupported MCP elicitation form",
			slog.String("mode", params.Tag), slog.Any("error", err))
		t.emitElicitationDeclinedNotice("the requested form cannot be rendered safely")
		return decline
	}
	flow, ok := t.runElicitationFlow(ctx, input)
	if !ok {
		return cancel
	}
	switch flow.Status {
	case userinput.StatusSubmitted:
		content, err := mapping.Content(flow)
		if err != nil {
			t.logger.Warn("codex: elicitation answers did not satisfy the form schema", slog.Any("error", err))
			return cancel
		}
		return protocol.McpServerElicitationRequestResponse{
			Action:  protocol.McpServerElicitationActionAccept,
			Content: content,
		}
	case userinput.StatusCanceled:
		if reason, _ := flow.Result["reason"].(string); strings.TrimSpace(reason) == "user_canceled" {
			return decline
		}
		return cancel
	default:
		return cancel
	}
}

// mcpConsentTarget extracts the server and tool a consent asks about from
// codex's consent message. The template is hard-coded in the pinned codex
// release ("Allow the <server> MCP server to run tool \"<tool>\"?"); a
// non-matching message reports no target and the consent falls through to
// the user's approval card (fail closed, never a false allow).
var mcpConsentMessagePattern = regexp.MustCompile(`^Allow the (\S+) MCP server to run tool "([^"]+)"\?$`)

func mcpConsentTarget(message string) (serverName, toolName string, ok bool) {
	match := mcpConsentMessagePattern.FindStringSubmatch(strings.TrimSpace(message))
	if match == nil {
		return "", "", false
	}
	return match[1], match[2], true
}

// runMCPToolConsent asks the user about an MCP tool-call consent the
// dispatch layer could not auto-accept — unknown servers, unknown tools,
// unparseable messages — as a permission approval.
func (t *turnState) runMCPToolConsent(ctx context.Context, message string, meta map[string]any) protocol.McpServerElicitationRequestResponse {
	decline := protocol.McpServerElicitationRequestResponse{Action: protocol.McpServerElicitationActionDecline}
	cancel := protocol.McpServerElicitationRequestResponse{Action: protocol.McpServerElicitationActionCancel}

	input := map[string]any{"message": strings.TrimSpace(message)}
	if params, ok := meta["tool_params"].(map[string]any); ok && len(params) > 0 {
		input["tool_params"] = params
	}
	if description, ok := meta["tool_description"].(string); ok && strings.TrimSpace(description) != "" {
		input["tool_description"] = strings.TrimSpace(description)
	}
	result := t.decide(ctx, "codex-consent-"+uuid.NewString(), "permission", input, nil)
	switch {
	case result.Approved:
		return protocol.McpServerElicitationRequestResponse{Action: protocol.McpServerElicitationActionAccept}
	case strings.EqualFold(result.Status, approval.StatusRejected):
		return decline
	default:
		return cancel
	}
}

// runURLElicitation asks the user to complete an out-of-band browser step.
// The MCP url mode carries no form content; the response is accept once the
// user confirms, decline when they cancel.
func (t *turnState) runURLElicitation(ctx context.Context, params *protocol.URLMcpServerElicitationRequestParams) protocol.McpServerElicitationRequestResponse {
	decline := protocol.McpServerElicitationRequestResponse{Action: protocol.McpServerElicitationActionDecline}
	url := strings.TrimSpace(params.URL)
	if url == "" {
		return decline
	}
	text := strings.TrimSpace(params.Message)
	if text == "" {
		text = "The agent needs you to complete a step in your browser"
	}
	input := map[string]any{"questions": []map[string]any{{
		"text": text + ": " + url,
		"kind": userinput.QuestionKindSingleSelect,
		"options": []map[string]any{
			{"label": "Done", "description": "I completed the step"},
			{"label": "Cancel", "description": "Do not continue"},
		},
	}}}
	flow, ok := t.runElicitationFlow(ctx, input)
	if !ok || flow.Status != userinput.StatusSubmitted {
		return decline
	}
	answers := userinput.AnswersFromResult(flow.Result)
	if len(answers) == 1 && len(answers[0].Selected) == 1 && answers[0].Selected[0].Label == "Done" {
		return protocol.McpServerElicitationRequestResponse{Action: protocol.McpServerElicitationActionAccept}
	}
	return decline
}

func (t *turnState) runElicitationFlow(ctx context.Context, input map[string]any) (userinput.Request, bool) {
	expiresAt := time.Now().Add(userinput.DefaultWaitTimeout + time.Minute)
	flow, err := userinput.RunFlow(ctx, t.userInput, userinput.FlowRequest{
		Input: userinput.CreatePendingInput{
			BotID:                        t.input.BotID,
			SessionID:                    t.input.ThreadID,
			RouteID:                      t.input.RouteID,
			ChannelIdentityID:            t.input.ChannelIdentityID,
			RequestedByChannelIdentityID: t.input.ChannelIdentityID,
			ToolCallID:                   "codex-elicitation-" + uuid.NewString(),
			ToolName:                     userinput.ToolNameAskUser,
			Input:                        input,
			ProviderMetadata: map[string]any{
				"source":    userinput.ProviderSourceCodexElicitation,
				"thread_id": t.threadID,
				"run_id":    t.input.RunID,
			},
			SourcePlatform:   t.input.CurrentPlatform,
			ReplyTarget:      t.input.ReplyTarget,
			ConversationType: t.input.ConversationType,
			ExpiresAt:        &expiresAt,
		},
		ActorChannelIdentityID: t.input.ChannelIdentityID,
		// The non-interactive case was rejected at runElicitation's entry.
		Interactive:          true,
		WaitTimeout:          userinput.DefaultWaitTimeout,
		Emit:                 t.emitUserInputRequest,
		NonInteractiveReason: "codex MCP elicitation requested user input without an interactive stream",
		UndeliveredReason:    "codex MCP elicitation was not delivered to the interactive stream",
		TimeoutReason:        "codex MCP elicitation timed out",
		AbortReason:          "codex MCP elicitation aborted",
	})
	if err != nil {
		if ctx.Err() == nil {
			t.logger.Error("codex MCP elicitation flow failed", slog.String("thread_id", t.threadID), slog.Any("error", err))
		}
		return userinput.Request{}, false
	}
	return flow.Request, true
}

// emitElicitationDeclinedNotice surfaces a declined MCP elicitation in the
// conversation so the user knows a tool asked for input Memoh could not show.
func (t *turnState) emitElicitationDeclinedNotice(reason string) {
	t.emit(event.StreamEvent{
		Type:  event.RuntimeNotice,
		Code:  "elicitation_declined",
		Delta: "A tool asked for user input that could not be shown: " + reason,
	})
}

// anyToSchemaMap coerces a decoded protocol schema (typed struct or free-form
// value) into the generic JSON-schema map the shared elicitation core reads.
func anyToSchemaMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]any); ok {
		return m
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil
	}
	return out
}
