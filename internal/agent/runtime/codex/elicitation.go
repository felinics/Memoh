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
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
)

// dispatchElicitation sends runtime requests to a uniquely owned turn.
// Even Memoh tool consents must reach a user or be declined.
func (s *appServer) dispatchElicitation(req *protocol.Inbound, params *protocol.McpServerElicitationRequestParams) {
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
	case protocol.McpServerElicitationRequestParamsTagOpenaiFormCamelCase:
		if params.OpenaiFormCamelCase == nil {
			return decline
		}
		message = params.OpenaiFormCamelCase.Message
		schema = anyToSchemaMap(params.OpenaiFormCamelCase.RequestedSchema)
		meta = anyToSchemaMap(params.OpenaiFormCamelCase.Meta)
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

// runMCPToolConsent forwards the runtime consent to the user.
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
