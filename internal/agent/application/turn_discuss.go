package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/memohai/memoh/internal/agent/runtime/session"
	"github.com/memohai/memoh/internal/agent/turn"
	sessionpkg "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/chat/timeline"
	"github.com/memohai/memoh/internal/models"
)

// turnRuntimeHooks are test seams for the transport-facing turn lifecycle.
// Production leaves them nil and calls the Service's own orchestration methods
// and native Agent directly.
type turnRuntimeHooks struct {
	streamChat       func(context.Context, ChatRequest) (<-chan StreamChunk, <-chan error)
	streamAgent      func(context.Context, native.RunConfig) <-chan native.StreamEvent
	resolveRunConfig func(context.Context, string, string, string, string, string, string, string) (ResolveRunConfigResult, error)
	inlineImages     func(context.Context, string, []timeline.ImageAttachmentRef) []sdk.ImagePart
	storeRound       func(context.Context, string, string, string, string, []sdk.Message, string) error
}

// startDiscussTurn orchestrates one discuss turn: resolve the run config,
// emit a synthetic run-resolved event, then either stream the native agent
// (persisting the round) or the external ACP runtime. The participation
// gate for ACP runtimes lives here because it is a property of runtime
// cost, not of channel policy: the caller supplies DiscussAddressed and
// the runtime decides whether starting is worth it.
// The run context and its admission are established by StartTurn, so a discuss
// turn occupies the thread's single slot on the same terms as a chat turn.
func (s *Service) startDiscussTurn(runCtx context.Context, cmd turn.StartTurnCommand, cancel context.CancelFunc, admission sessionruntime.Admission) (turn.RunHandle, error) {
	if !s.discussRuntimeConfigured() {
		return nil, errors.New("turn: discuss runtime not configured")
	}
	h := newDiscussHandle(runCtx, cmd, cancel, admission.RunID, s.turnRunFinisher(runCtx, admission))
	h.publishAgentEvent = s.turnAgentEventPublisher(admission.Handle)
	go s.pumpDiscuss(runCtx, cmd, h)
	return h, nil
}

func newDiscussHandle(ctx context.Context, cmd turn.StartTurnCommand, cancel context.CancelFunc, runID string, finishRun func(status string, cause error)) *discussHandle {
	return &discussHandle{
		runHandle: runHandle{
			id:        runID,
			events:    make(chan turn.Event, 16),
			errs:      make(chan error, 1),
			ctx:       ctx,
			cancel:    cancel,
			inject:    make(chan turn.InjectMessage), // unused in discuss mode
			addAssets: func([]turn.OutboundAssetRef) {},
			finishRun: finishRun,
		},
		teamID:    cmd.TeamID,
		sessionID: cmd.ThreadID,
	}
}

// Inject is not supported in discuss mode: no reader consumes the inject
// channel, so blocking until the run ends would just wedge the caller.
// Shadowing runHandle.Inject fails fast instead.
func (*discussHandle) Inject(context.Context, turn.InjectMessage) error {
	return errors.New("turn: discuss turns do not accept injected messages")
}

// discussHandle reuses runHandle's channel pair with manual event emission.
type discussHandle struct {
	runHandle
	teamID    string
	sessionID string
	seq       int64
}

// emit delivers one event, giving up when the run context is canceled so
// a stalled consumer can never wedge the pump (Cancel must always unblock).
func (h *discussHandle) emit(kind string, payload []byte) bool {
	h.seq++
	select {
	case h.events <- turn.Event{
		RunID:    h.id,
		TeamID:   h.teamID,
		ThreadID: h.sessionID,
		Seq:      h.seq,
		Kind:     kind,
		Payload:  payload,
	}:
		return true
	case <-h.ctx.Done():
		h.failed.Store(true)
		return false
	}
}

// emitErr mirrors emit for the error channel. Any reported error marks the run
// failed; recordStreamFailure preserves explicit cancellation as an abort.
func (h *discussHandle) emitErr(err error) bool {
	if !h.recordStreamFailure(err) {
		return false
	}
	select {
	case h.errs <- err:
		return true
	case <-h.ctx.Done():
		return false
	}
}

func (s *Service) pumpDiscuss(ctx context.Context, cmd turn.StartTurnCommand, h *discussHandle) {
	defer close(h.events)
	defer close(h.errs)
	defer h.finish()
	defer func() {
		// External cancellation can surface as a cleanly closed agent
		// stream; record it before cancel() masks the distinction.
		if h.ctx.Err() != nil {
			h.failed.Store(true)
		}
		h.cancel()
	}()

	resolved, err := s.resolveDiscussRunConfig(ctx,
		cmd.BotID, cmd.ThreadID, cmd.SourceChannelIdentityID,
		cmd.CurrentChannel, cmd.ReplyTarget, cmd.ConversationType, cmd.SessionToken)
	if err != nil {
		h.emitErr(err)
		return
	}
	resolvedPayload, _ := json.Marshal(turn.DiscussRunResolvedPayload{RuntimeType: resolved.RuntimeType})
	if !h.emit(turn.DiscussEventRunResolved, resolvedPayload) {
		return
	}

	if strings.TrimSpace(resolved.RuntimeType) == sessionpkg.RuntimeACPAgent {
		if !cmd.DiscussAddressed {
			h.emit(turn.DiscussEventSkipped, nil)
			return
		}
		s.pumpDiscussACP(ctx, cmd, h)
		return
	}
	s.pumpDiscussNative(ctx, cmd, h, resolved)
}

func (s *Service) pumpDiscussNative(ctx context.Context, cmd turn.StartTurnCommand, h *discussHandle, resolved ResolveRunConfigResult) {
	runConfig := resolved.RunConfig
	runConfig.Messages = discussMessagesToSDK(cmd.DiscussMessages)
	runConfig.SessionType = sessionpkg.TypeDiscuss
	runConfig.Query = ""
	runConfig.ContextCurrentUserMessageIndex = nil
	runConfig.ContextMemoryMessageIndex = nil
	runConfig.ContextSourceFrags = nil

	// Inline image attachments from new RC segments so the model receives
	// them as native vision input (ImagePart) on the first encounter.
	if runConfig.SupportsImageInput && len(cmd.DiscussImageRefs) > 0 {
		refs := make([]timeline.ImageAttachmentRef, len(cmd.DiscussImageRefs))
		for i, r := range cmd.DiscussImageRefs {
			refs[i] = timeline.ImageAttachmentRef{ContentHash: r.ContentHash, Mime: r.Mime}
		}
		imageParts := s.inlineDiscussImages(ctx, cmd.BotID, refs)
		injectImagePartsIntoLastUserMessage(runConfig.Messages, imageParts)
	}
	runConfig = runConfig.RefreshContextFrag()

	eventCh := s.streamDiscussAgent(ctx, runConfig)

	var finalMessages json.RawMessage
	var terminalEvent native.StreamEvent
	var terminalPayload []byte
	var hasTerminalEvent bool
	for event := range eventCh {
		terminal := event.Type == native.EventAgentEnd || event.Type == native.EventAgentAbort
		if terminal {
			finalMessages = event.Messages
			terminalEvent = event
			terminalPayload, _ = json.Marshal(event)
			hasTerminalEvent = true
			continue
		}
		if h.publishAgentEvent != nil {
			if publishErr := h.publishAgentEvent(ctx, event); publishErr != nil {
				h.emitErr(publishErr)
				return
			}
		}
		payload, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			continue
		}
		if !h.emit(string(event.Type), payload) {
			return
		}
	}

	if len(finalMessages) > 0 {
		var sdkMsgs []sdk.Message
		if json.Unmarshal(finalMessages, &sdkMsgs) == nil && len(sdkMsgs) > 0 {
			if storeErr := s.storeDiscussRound(ctx,
				cmd.BotID, cmd.ThreadID, cmd.SourceChannelIdentityID, cmd.CurrentChannel,
				sdkMsgs, resolved.ModelID,
			); storeErr != nil {
				h.emitErr(runtimeHistoryError(storeErr))
				return
			}
		}
	}
	if hasTerminalEvent && h.publishAgentEvent != nil {
		if publishErr := h.publishAgentEvent(ctx, terminalEvent); publishErr != nil {
			h.emitErr(publishErr)
			return
		}
		h.terminalPublished = true
	}
	if len(terminalPayload) > 0 && !h.emit(string(terminalEvent.Type), terminalPayload) {
		return
	}

	// Compute pressure on this goroutine so the detached trigger holds a few
	// scalars instead of pinning the whole composed context until it runs.
	if compactable := discussCompactableTokens(cmd.DiscussMessages); compactable > 0 && s.compactionService != nil && s.settingsService != nil {
		go s.maybeCompactDiscuss(context.WithoutCancel(ctx), cmd.BotID, cmd.ThreadID, resolved.ModelID, compactable)
	}
}

// maybeCompactDiscuss re-evaluates compaction pressure after a native discuss
// turn with the same trigger policy as the chat path. ACP discuss turns run
// through streamTurnChat and inherit its trigger directly.
func (s *Service) maybeCompactDiscuss(ctx context.Context, botID, threadID, modelID string, compactable int) {
	budget := 0
	var turnModel models.GetResponse
	if s.modelsService != nil && strings.TrimSpace(modelID) != "" {
		if model, err := s.modelsService.GetByID(ctx, modelID); err == nil {
			turnModel = model
			if model.Config.ContextWindow != nil {
				budget = *model.Config.ContextWindow
			}
		}
	}
	s.maybeCompact(ctx, ChatRequest{BotID: botID, ThreadID: threadID}, resolvedContext{
		model:                  turnModel,
		compactableTokens:      compactable,
		compactableTokensKnown: true,
		contextTokenBudget:     budget,
	}, compactable)
}

// discussCompactableTokens estimates the raw history share of a discuss
// context, excluding artifact summaries, in the chat trigger's token unit.
func discussCompactableTokens(messages []turn.DiscussMessage) int {
	total := 0
	for _, message := range messages {
		if message.CompactionArtifactID != "" {
			continue
		}
		size := len(message.RawContent)
		if size == 0 {
			size = len(message.Content)
		}
		total += size / 4
	}
	return total
}

func (s *Service) pumpDiscussACP(ctx context.Context, cmd turn.StartTurnCommand, h *discussHandle) {
	prompt := discussACPFullContextPrompt(cmd.DiscussMessages)
	if strings.TrimSpace(prompt) == "" {
		// No composable context: end without a skip marker so the caller
		// does not advance its consumed cursor (pre-port semantics).
		return
	}
	chunks, errs := s.streamTurnChat(ctx, ChatRequest{
		BotID:                   cmd.BotID,
		ChatID:                  cmd.BotID,
		ThreadID:                cmd.ThreadID,
		RouteID:                 cmd.RouteID,
		SourceChannelIdentityID: cmd.SourceChannelIdentityID,
		CurrentChannel:          cmd.CurrentChannel,
		ReplyTarget:             cmd.ReplyTarget,
		ConversationType:        cmd.ConversationType,
		Token:                   cmd.SessionToken,
		ChatToken:               cmd.ChatToken,
		ToolHTTPURL:             cmd.ToolHTTPURL,
		Query:                   prompt,
		RawQuery:                prompt,
		UserMessagePersisted:    true,
		SkipMemoryExtraction:    true,
		ForceFreshRuntime:       true,
	})
	for chunks != nil || errs != nil {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			if !h.emit(parseKind(chunk), chunk) {
				return
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				if !h.emitErr(err) {
					return
				}
			}
		case <-ctx.Done():
			h.failed.Store(true)
			return
		}
	}
}

func (s *Service) discussRuntimeConfigured() bool {
	if s == nil {
		return false
	}
	if s.turnHooks != nil && s.turnHooks.resolveRunConfig != nil {
		return s.turnHooks.streamAgent != nil
	}
	return s.agent != nil
}

func (s *Service) resolveDiscussRunConfig(
	ctx context.Context,
	botID, sessionID, channelIdentityID, currentPlatform, replyTarget, conversationType, chatToken string,
) (ResolveRunConfigResult, error) {
	if s.turnHooks != nil && s.turnHooks.resolveRunConfig != nil {
		return s.turnHooks.resolveRunConfig(
			ctx,
			botID,
			sessionID,
			channelIdentityID,
			currentPlatform,
			replyTarget,
			conversationType,
			chatToken,
		)
	}
	return s.ResolveRunConfig(
		ctx,
		botID,
		sessionID,
		channelIdentityID,
		currentPlatform,
		replyTarget,
		conversationType,
		chatToken,
	)
}

func (s *Service) inlineDiscussImages(ctx context.Context, botID string, refs []timeline.ImageAttachmentRef) []sdk.ImagePart {
	if s.turnHooks != nil && s.turnHooks.inlineImages != nil {
		return s.turnHooks.inlineImages(ctx, botID, refs)
	}
	return s.InlineImageAttachments(ctx, botID, refs)
}

func (s *Service) streamDiscussAgent(ctx context.Context, cfg native.RunConfig) <-chan native.StreamEvent {
	if s.turnHooks != nil && s.turnHooks.streamAgent != nil {
		return s.turnHooks.streamAgent(ctx, cfg)
	}
	return s.agent.Stream(ctx, cfg)
}

func (s *Service) storeDiscussRound(
	ctx context.Context,
	botID, sessionID, channelIdentityID, currentPlatform string,
	messages []sdk.Message,
	modelID string,
) error {
	if s.turnHooks != nil && s.turnHooks.storeRound != nil {
		return s.turnHooks.storeRound(
			ctx,
			botID,
			sessionID,
			channelIdentityID,
			currentPlatform,
			messages,
			modelID,
		)
	}
	return s.StoreRound(ctx, botID, sessionID, channelIdentityID, currentPlatform, messages, modelID)
}

// discussMessagesToSDK converts composed context messages into SDK
// messages, preserving structured raw content when present.
func discussMessagesToSDK(messages []turn.DiscussMessage) []sdk.Message {
	result := make([]sdk.Message, 0, len(messages))
	for _, m := range messages {
		if len(m.RawContent) > 0 {
			raw, err := json.Marshal(struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}{
				Role:    m.Role,
				Content: m.RawContent,
			})
			if err == nil {
				var msg sdk.Message
				if json.Unmarshal(raw, &msg) == nil {
					result = append(result, msg)
					continue
				}
			}
		}
		switch m.Role {
		case "assistant":
			result = append(result, sdk.AssistantMessage(m.Content))
		default:
			result = append(result, sdk.UserMessage(m.Content))
		}
	}
	return result
}

// injectImagePartsIntoLastUserMessage appends ImageParts to the last user
// message in msgs so the model receives inline vision input.
func injectImagePartsIntoLastUserMessage(msgs []sdk.Message, parts []sdk.ImagePart) {
	if len(parts) == 0 {
		return
	}
	extra := make([]sdk.MessagePart, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p.Image) != "" {
			extra = append(extra, p)
		}
	}
	if len(extra) == 0 {
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == sdk.MessageRoleUser {
			msgs[i].Content = append(msgs[i].Content, extra...)
			return
		}
	}
}

// discussACPFullContextPrompt renders the composed context into the single
// reset-each-turn prompt used by external ACP runtimes. ACP does not receive
// native ToolUsage, so its stable preamble owns the send-only output contract.
func discussACPFullContextPrompt(messages []turn.DiscussMessage) string {
	var b strings.Builder
	b.WriteString("You are replying in a discuss-mode conversation. The runtime is reset each turn, so use the complete context below as the source of truth.\n\n")
	b.WriteString("IMPORTANT: You MUST use the `send` tool to speak in the observed conversation. Ordinary text output is internal and invisible to everyone.\n\n")
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		b.WriteString("[")
		b.WriteString(role)
		b.WriteString("]\n")
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	b.WriteString("Reply to the latest user-visible message when a response is appropriate.")
	return b.String()
}
