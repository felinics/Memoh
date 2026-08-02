package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/memohai/memoh/internal/agent/runtime/session"
	"github.com/memohai/memoh/internal/agent/turn"
	sessionpkg "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/chat/timeline"
	"github.com/memohai/memoh/internal/textutil"
)

const (
	discussForceReplyInstruction = "Operator directive: channel policy requires a reply to the latest addressed incoming message. You must send a concise, relevant reply through the available messaging capability. Do not stay silent or respond only in private text."
	discussForceReplySignal      = `<operator-directive force_reply="true"/>`
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
	compactDiscuss   func(context.Context, string, string, string, int, int, []string)
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

// emitErr mirrors emit for the error channel. Any reported error marks the
// run failed so finish releases the idempotency claim.
func (h *discussHandle) emitErr(err error) bool {
	h.failed.Store(true)
	if h.streamErr == nil {
		// Keep the first error: without it the terminal record cannot tell a
		// discuss turn that broke from one that was stopped.
		h.streamErr = err
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
	messageTokenBudget := resolved.DiscussMessageTokenBudget
	if messageTokenBudget <= 0 {
		messageTokenBudget = discussMessageTokenBudget(resolved.ContextTokenBudget)
	}
	contextMessages := trimDiscussMessagesToTokenBudget(s.logger, cmd.DiscussMessages, messageTokenBudget)
	runConfig.Messages = projectSDKMessageHeaders(
		projectDiscussToolHistory(discussMessagesToSDK(contextMessages)),
		runConfig.ChannelPolicy.MessageMetadataMode,
	)
	runConfig.SessionType = sessionpkg.TypeDiscuss
	runConfig.Query = ""

	// Resolve image attachments from new RC segments on first encounter. A
	// vision-capable primary model receives them directly; otherwise the global
	// auxiliary vision model describes them and the observation is appended to
	// the latest user message.
	if len(cmd.DiscussImageRefs) > 0 &&
		(runConfig.SupportsImageInput || s.auxiliaryVisionConfigSnapshot().enabled()) {
		refs := make([]timeline.ImageAttachmentRef, len(cmd.DiscussImageRefs))
		for i, r := range cmd.DiscussImageRefs {
			refs[i] = timeline.ImageAttachmentRef{ContentHash: r.ContentHash, Mime: r.Mime}
		}
		imageParts := s.inlineDiscussImages(ctx, cmd.BotID, refs)
		if runConfig.SupportsImageInput {
			injectImagePartsIntoLastUserMessage(runConfig.Messages, imageParts)
		} else {
			visionContext := s.describeImagePartsWithAuxiliaryVision(ctx, ChatRequest{
				BotID:  cmd.BotID,
				UserID: cmd.UserID,
				Query:  lastUserSDKMessageText(runConfig.Messages),
			}, false, imageParts)
			runConfig.Messages = appendAuxiliaryVisionToLastUserMessage(runConfig.Messages, visionContext)
		}
	}
	if cmd.DiscussForceReply {
		// Keep the stable system/tools prefix identical for forced and sampled
		// turns. The static discuss contract defines this service-only signal;
		// appending it after media enrichment keeps images attached to the real
		// incoming message. The signal is removed before persistence below. The
		// request-level tool choice enforces the same boundary without changing
		// the cacheable prompt prefix; native only applies it when send is exposed.
		runConfig.RequireToolCall = true
		runConfig.Messages = append(runConfig.Messages, sdk.UserMessage(discussForceReplySignal))
	}
	runConfig = runConfig.RefreshContextFrag()

	eventCh := s.streamDiscussAgent(ctx, runConfig)

	var finalMessages json.RawMessage
	for event := range eventCh {
		if event.Type == native.EventAgentEnd || event.Type == native.EventAgentAbort {
			finalMessages = event.Messages
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
			sdkMsgs = removeDiscussForceReplySignal(sdkMsgs)
			if storeErr := s.storeDiscussRound(ctx,
				cmd.BotID, cmd.ThreadID, cmd.SourceChannelIdentityID, cmd.CurrentChannel,
				sdkMsgs, resolved.ModelID,
			); storeErr != nil {
				h.emitErr(storeErr)
			}
		}
	}

	s.triggerDiscussCompaction(ctx, cmd, resolved.ContextTokenBudget)
}

func removeDiscussForceReplySignal(messages []sdk.Message) []sdk.Message {
	for i := range messages {
		if !isDiscussForceReplySignal(messages[i]) {
			continue
		}
		out := make([]sdk.Message, 0, len(messages)-1)
		out = append(out, messages[:i]...)
		out = append(out, messages[i+1:]...)
		return out
	}
	return messages
}

func isDiscussForceReplySignal(message sdk.Message) bool {
	if message.Role != sdk.MessageRoleUser || len(message.Content) != 1 {
		return false
	}
	text, ok := message.Content[0].(sdk.TextPart)
	return ok && strings.TrimSpace(text.Text) == discussForceReplySignal
}

// triggerDiscussCompaction computes pressure and snapshots the visible
// compaction frontier before detaching, so a queued background trigger can
// detect that its context became stale while another compaction completed.
func (s *Service) triggerDiscussCompaction(ctx context.Context, cmd turn.StartTurnCommand, contextTokenBudget int) {
	inputTokens := discussCompactableTokens(cmd.DiscussMessages)
	if inputTokens <= 0 {
		return
	}
	observedArtifactIDs := discussCompactionArtifactIDs(cmd.DiscussMessages)
	if s.turnHooks != nil && s.turnHooks.compactDiscuss != nil {
		s.turnHooks.compactDiscuss(ctx, cmd.BotID, cmd.ThreadID, cmd.UserID, inputTokens, contextTokenBudget, observedArtifactIDs)
		return
	}
	if s.compactionService == nil || s.settingsService == nil {
		return
	}

	go s.maybeCompactDiscuss(
		context.WithoutCancel(ctx),
		cmd.BotID,
		cmd.ThreadID,
		cmd.UserID,
		inputTokens,
		contextTokenBudget,
		observedArtifactIDs,
	)
}

// maybeCompactDiscuss re-evaluates compaction pressure after either native or
// ACP discuss turns with the same trigger policy as the chat path.
func (s *Service) maybeCompactDiscuss(ctx context.Context, botID, threadID, userID string, inputTokens, contextTokenBudget int, observedArtifactIDs []string) {
	s.maybeCompact(ctx, ChatRequest{BotID: botID, ThreadID: threadID, UserID: userID}, resolvedContext{
		compactionInputTokens:      inputTokens,
		compactionInputTokensKnown: true,
		contextTokenBudget:         contextTokenBudget,
		compactionArtifactIDs:      append([]string(nil), observedArtifactIDs...),
		compactionArtifactsKnown:   true,
	}, inputTokens)
}

// discussCompactableTokens estimates the effective rolling context: the active
// summary plus all raw history accumulated since it. The historical function
// name is retained for compatibility with focused tests.
func discussCompactableTokens(messages []turn.DiscussMessage) int {
	total := 0
	for _, message := range messages {
		total += estimateDiscussMessageTokens(message)
	}
	return total
}

func discussCompactionArtifactIDs(messages []turn.DiscussMessage) []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, message := range messages {
		id := strings.TrimSpace(message.CompactionArtifactID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// trimDiscussMessagesByTokens keeps the newest context within the same 70%
// budget used by the synchronous compaction backstop, leaving room for the
// system prompt, tools and model output. Active summaries remain pinned because
// they are the only representation of their covered ranges.
func trimDiscussMessagesByTokens(log *slog.Logger, messages []turn.DiscussMessage, contextTokenBudget int) []turn.DiscussMessage {
	return trimDiscussMessagesToTokenBudget(log, messages, discussMessageTokenBudget(contextTokenBudget))
}

func trimDiscussMessagesToTokenBudget(log *slog.Logger, messages []turn.DiscussMessage, maxTokens int) []turn.DiscussMessage {
	if maxTokens <= 0 || len(messages) == 0 {
		return messages
	}

	estimatedTokens := 0
	for _, message := range messages {
		estimatedTokens += estimateDiscussMessageTokens(message)
	}
	if estimatedTokens <= maxTokens {
		return messages
	}

	scannedTokens := 0
	cutoff := 0
	for i := len(messages) - 1; i >= 0; i-- {
		scannedTokens += estimateDiscussMessageTokens(messages[i])
		if scannedTokens > maxTokens {
			cutoff = i + 1
			break
		}
	}
	for cutoff < len(messages) && strings.EqualFold(strings.TrimSpace(messages[cutoff].Role), "tool") {
		cutoff++
	}
	preservedLatestTurn := false
	if cutoff >= len(messages) {
		// The entire nominally retained suffix consists of tool results.
		// Preserve the latest user turn (or at least the latest non-tool
		// message) with that suffix instead of sending summaries alone.
		cutoff = latestSafeDiscussCutoff(messages)
		preservedLatestTurn = true
	}
	if cutoff == 0 {
		if log != nil {
			log.Warn("trimDiscussMessagesByTokens: context exceeds reserved budget; preserving latest turn",
				slog.Int("total_messages", len(messages)),
				slog.Int("estimated_tokens", estimatedTokens),
				slog.Int("message_token_budget", maxTokens),
			)
		}
		return messages
	}

	kept := make([]turn.DiscussMessage, 0, len(messages)-cutoff)
	for i := 0; i < cutoff; i++ {
		if strings.TrimSpace(messages[i].CompactionArtifactID) != "" {
			kept = append(kept, messages[i])
		}
	}
	kept = append(kept, messages[cutoff:]...)

	if log != nil {
		retainedTokens := 0
		for _, message := range kept {
			retainedTokens += estimateDiscussMessageTokens(message)
		}
		log.Warn("trimDiscussMessagesByTokens: context trimmed",
			slog.Int("total_messages", len(messages)),
			slog.Int("dropped_messages", len(messages)-len(kept)),
			slog.Int("estimated_tokens", estimatedTokens),
			slog.Int("retained_estimated_tokens", retainedTokens),
			slog.Int("message_token_budget", maxTokens),
			slog.Bool("preserved_latest_turn", preservedLatestTurn),
		)
	}
	return kept
}

func discussMessageTokenBudget(contextTokenBudget int) int {
	if contextTokenBudget <= 0 {
		return 0
	}
	budget := contextTokenBudget * compactionBudgetThresholdPercent / 100
	if budget == 0 {
		return 1
	}
	if budget > maxDiscussMessageTokenBudget {
		return maxDiscussMessageTokenBudget
	}
	return budget
}

// effectiveDiscussMessageTokenBudget also honors the bot's enabled compaction
// threshold as a pre-send ceiling. This keeps large group timelines within the
// operating limit the user selected even when a provider advertises a much
// larger theoretical model window than its compatibility endpoint accepts.
func effectiveDiscussMessageTokenBudget(contextTokenBudget int, compactionEnabled bool, compactionThreshold int) int {
	budget := discussMessageTokenBudget(contextTokenBudget)
	if !compactionEnabled || compactionThreshold <= 0 {
		return budget
	}
	if budget <= 0 || compactionThreshold < budget {
		return compactionThreshold
	}
	return budget
}

func latestSafeDiscussCutoff(messages []turn.DiscussMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(messages[i].Role), "user") {
			return i
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if !strings.EqualFold(strings.TrimSpace(messages[i].Role), "tool") {
			return i
		}
	}
	return 0
}

func estimateDiscussMessageTokens(message turn.DiscussMessage) int {
	return textutil.EstimateTokensFromBytes(discussMessageContentBytes(message))
}

func discussMessageContentBytes(message turn.DiscussMessage) int {
	if len(message.RawContent) > 0 {
		return len(message.RawContent)
	}
	return len(message.Content)
}

func (s *Service) pumpDiscussACP(ctx context.Context, cmd turn.StartTurnCommand, h *discussHandle) {
	prompt := discussACPFullContextPrompt(cmd.DiscussMessages)
	if cmd.DiscussForceReply {
		prompt = strings.TrimSpace(prompt + "\n\n" + discussForceReplyInstruction)
	}
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
		UserID:                  cmd.UserID,
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
	s.triggerDiscussCompaction(ctx, cmd, 0)
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
		return filterVisionImageParts(s.turnHooks.inlineImages(ctx, botID, refs))
	}
	return filterVisionImageParts(s.InlineImageAttachments(ctx, botID, refs))
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
	parts = filterVisionImageParts(parts)
	extra := make([]sdk.MessagePart, 0, len(parts))
	for _, p := range parts {
		extra = append(extra, p)
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
