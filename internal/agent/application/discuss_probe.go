package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthctx"
	"github.com/felinics/memoh/internal/providers"
)

const (
	discussProbeTimeout = 45 * time.Second
	// discussProbeMaxTokens caps the probe completion. Same two-sided bound as
	// title generation: reasoning models burn hidden thinking before emitting
	// the tool call, so a small cap truncates the decision away, while a large
	// cap makes short-context models reject the request outright.
	discussProbeMaxTokens = 2048
	// discussProbeContextMaxTokens caps the judge's view independently of the
	// primary budget: "should the bot speak right now" is answered from the
	// recent conversational surface, not from the whole thread, and the gate
	// must not re-walk the cliff the primary path OOM'd on.
	discussProbeContextMaxTokens = 16384
	// discussProbePromptOverheadTokens reserves room for the judge prompt and
	// the decide tool definition when sizing input against the judge's own
	// context window.
	discussProbePromptOverheadTokens = 1024
	// discussProbeContextFloorTokens is the smallest input window worth sending.
	// A judge that cannot be given this much has nothing to judge from, which is
	// a configuration fault to report rather than a request to send hopefully.
	// It is a floor on usefulness, never a floor the window may exceed.
	discussProbeContextFloorTokens = 2048
	// discussProbeFallbackWindow is the window assumed when the catalog declares
	// none. 8k sits below virtually every chat model's real window, so the
	// reservation stays valid rather than optimistic — the same reasoning as
	// titleInputFallbackWindow.
	discussProbeFallbackWindow = 8192

	discussProbeToolName = "decide"
	// discussProbeForcedToolChoice is the one spelling every provider adapter
	// converts correctly. `decide` is the only tool on a probe request, so
	// "required" and a named-function choice are equivalent in intent — but the
	// Chat Completions object form is not portable: the Responses adapter
	// forwards it verbatim to an API that expects a top-level name, and the
	// Gemini adapter recognises only string choices and silently drops the
	// object, leaving the judge free to answer in prose. Both land as
	// missing/malformed and gate the bot shut.
	discussProbeForcedToolChoice = "required"

	// should_act values. Deliberately two-valued: there is no "react only"
	// option, because a standalone reaction must not wake the primary.
	discussProbeActSend     = "send"
	discussProbeActNoAction = "no_action"

	// Outcomes recorded for audit. Everything other than "act" leaves the gate
	// closed; the distinct values exist so a silent bot can be diagnosed as a
	// judgement, a parse failure, or an outage.
	discussProbeOutcomeAct       = "act"
	discussProbeOutcomeNoAction  = "no_action"
	discussProbeOutcomeMissing   = "missing"
	discussProbeOutcomeMalformed = "malformed"
	discussProbeOutcomeError     = "error"

	// discussProbeCauseNotConfigured is the one exit that is ordinary enough to
	// log quietly; every other disabled path is a fault worth surfacing.
	discussProbeCauseNotConfigured = "not_configured"
)

// discussProbeResult is the gate's verdict for one discuss wake-up.
type discussProbeResult struct {
	// Ran reports whether a probe model was configured and actually judged.
	// When false the gate is disabled and the caller proceeds unchanged.
	Ran       bool
	Activated bool
	Reason    string
	Outcome   string
}

// discussProbeVerdict runs the gate through the test seam when one is
// installed, mirroring the other turn hooks.
func (s *Service) discussProbeVerdict(ctx context.Context, cmd turn.StartTurnCommand, resolved ResolveRunConfigResult) discussProbeResult {
	if s.turnHooks != nil && s.turnHooks.discussProbe != nil {
		return s.turnHooks.discussProbe(ctx, cmd, resolved)
	}
	return s.runDiscussProbe(ctx, cmd, resolved)
}

// resolveDiscussProbeModel picks the model that judges discuss wake-ups:
// the bot's own probe model when set, otherwise the owner's title model.
//
// The fallback is deliberate rather than incidental. Both slots want the same
// kind of model — cheap, fast, no tools of its own — so an operator who already
// chose one for titles gets a working gate without a second decision. The
// bot-level setting still wins, because group-chat policy is per-bot while the
// title model is an account-wide preference.
//
// botConfigured comes from the settings the run-config builder already read, so
// the common path costs one bot row for the owner and nothing else. Only the
// fallback pays for the owner's account profile.
//
// An empty model ID is not an error: it means the gate is disabled.
func (s *Service) resolveDiscussProbeModel(ctx context.Context, botID, botConfigured string) (modelID, ownerUserID string, err error) {
	if configured := strings.TrimSpace(botConfigured); configured != "" {
		ownerUserID, err = s.resolveBotOwnerUserID(ctx, botID)
		if err != nil {
			return "", "", err
		}
		return configured, ownerUserID, nil
	}
	return s.resolveTitleModel(ctx, botID)
}

// discussProbeModelCanJudge reports whether a resolved model can actually
// return a verdict.
//
// Tool calling is the whole channel: the judge's only output is a forced
// `decide` call. A model without it answers in prose on every wake-up, which
// the gate reads as a missing verdict and fails closed — a bot that goes
// permanently silent with no error anyone would think to look for.
//
// This guard is load-bearing rather than defensive. The picker filters the
// explicit choice for tool calling, but the inherited path does not go through
// the picker: a title model is only validated as type=chat (see
// Store.IsValidTitleModel), so a perfectly legitimate text-only title model can
// arrive here as the gate. When it does, the right answer is to leave the gate
// off, not to hold it shut.
func discussProbeModelCanJudge(model models.GetResponse) bool {
	return model.HasCompatibility(models.CompatToolCall)
}

// discussProbeGenerateOptions builds the judge's request.
//
// It exists as a named function rather than an inline option list so the
// provider-request tests drive the same construction production does. Asserting
// the wire shape against a request the test assembled itself would only prove
// that the SDK converts a constant correctly, not that the gate sends it.
//
// MaxSteps stays at its zero default: one call, no tool auto-execution.
// `decide` reports a judgement; there is nothing to execute.
func discussProbeGenerateOptions(model *sdk.Model, system string, messages []sdk.Message, tools []sdk.Tool) []sdk.GenerateOption {
	return []sdk.GenerateOption{
		sdk.WithModel(model),
		sdk.WithSystem(system),
		sdk.WithMessages(messages),
		sdk.WithTools(tools),
		sdk.WithToolChoice(discussProbeForcedToolChoice),
		sdk.WithMaxTokens(discussProbeMaxTokens),
	}
}

// discussProbeContextBudget sizes the judge's input window, or reports that no
// valid request exists for this model.
//
// The reservation is a hard cap, not a suggestion: input plus the output cap
// plus the prompt and tool-schema overhead must all fit inside the judge's own
// context window. An earlier version clamped the input *up* to a floor, which
// made a 4097-token model ask for 5120 and be rejected on every wake-up — the
// gate then failed closed forever on what was really a configuration mistake.
//
// When the window cannot hold even a minimal request the answer is ok=false.
// Handing the provider a request already known to exceed its limit, to find out
// from the error, is not a check.
func discussProbeContextBudget(contextWindowTokens int) (budget int, ok bool) {
	window := contextWindowTokens
	if window <= 0 {
		window = discussProbeFallbackWindow
	}
	available := window - discussProbeMaxTokens - discussProbePromptOverheadTokens
	if available < discussProbeContextFloorTokens {
		return 0, false
	}
	if available > discussProbeContextMaxTokens {
		available = discussProbeContextMaxTokens
	}
	return available, true
}

// admitDiscussProbeMessages selects the newest messages that fit the judge's
// window, as a contiguous suffix that is a valid standalone history.
//
// It deliberately does NOT reuse admitDiscussMessages. That one pins every
// compaction summary because the primary must not lose thread history — but
// summaries are sized against the primary's window, so on a long thread their
// combined cost alone exceeds any judge-sized budget and admission returns
// ProtectedOverflow forever. The gate would then fail closed on every wake-up
// with no way back: a declined wake-up returns before sync compaction runs, so
// the summaries that caused the overflow are never reduced, and no amount of
// new messages — not even a direct mention — recovers it.
//
// The judge does not need thread history to answer "should the bot speak right
// now"; it needs the tail. Dropping the pin removes the overflow class rather
// than handling it.
//
// Two shapes still have to be enforced, and a size-only suffix enforces
// neither. A window may not open on a tool response whose tool call was cut:
// that is not a history any provider accepts, so the request fails as a
// protocol error rather than returning a judgement. And the window may not
// exceed the budget just because its last message does; an oversized newest
// message is truncated instead, since "what was just said" is the one thing the
// judge cannot do without.
//
// An empty return means no valid window exists, and the caller fails closed.
func admitDiscussProbeMessages(messages []turn.DiscussMessage, budgetTokens int) []turn.DiscussMessage {
	if len(messages) == 0 || budgetTokens <= 0 {
		return nil
	}

	start := len(messages) - 1
	used := discussMessageTokens(messages[start])
	for i := start - 1; i >= 0; i-- {
		cost := discussMessageTokens(messages[i])
		if used+cost > budgetTokens {
			break
		}
		used += cost
		start = i
	}

	// Mirrors the raw-window trim in turn.AdmitContextEntries: advance the start
	// past any tool response whose originating call is no longer in the window.
	for start < len(messages) && isDiscussToolResponse(messages[start]) {
		start++
	}
	if start >= len(messages) {
		return nil
	}

	window := messages[start:]
	if len(window) == 1 && discussMessageTokens(window[0]) > budgetTokens {
		truncated, ok := truncateDiscussProbeMessage(window[0], budgetTokens)
		if !ok {
			return nil
		}
		return []turn.DiscussMessage{truncated}
	}
	return window
}

func isDiscussToolResponse(message turn.DiscussMessage) bool {
	return strings.EqualFold(strings.TrimSpace(message.Role), "tool")
}

// truncateDiscussProbeMessage shrinks a single oversized message to the budget.
//
// Only plain text is reduced. A message carrying RawContent is a structured
// provider payload — tool calls and their arguments — and cutting bytes out of
// it yields something no provider will parse. Those report false so the caller
// fails closed with a cause, rather than sending a request that is malformed
// instead of merely large.
//
// The head/tail sample mirrors boundTitleInput: a long paste may state its
// subject at either end, and a pure head cut drops the part that usually
// decides whether the bot was addressed at all.
func truncateDiscussProbeMessage(message turn.DiscussMessage, budgetTokens int) (turn.DiscussMessage, bool) {
	if len(message.RawContent) > 0 {
		return turn.DiscussMessage{}, false
	}
	runes := []rune(message.Content)
	// discussMessageTokens estimates from byte length; holding to one rune per
	// token keeps the truncated result from creeping back over budget.
	if budgetTokens <= 0 || len(runes) <= budgetTokens {
		return message, true
	}
	head := budgetTokens * 2 / 3
	tail := budgetTokens - head
	message.Content = string(runes[:head]) + "\n…\n" + string(runes[len(runes)-tail:])
	return message, true
}

// discussProbeTool is the probe's only move. It carries no side effect: the
// runner never executes it, it is read back out of the response.
func discussProbeTool() sdk.Tool {
	return sdk.Tool{
		Name: discussProbeToolName,
		Description: "Report your judgement on whether the bot should act in this conversation right now. " +
			"This is your only output.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"should_act": map[string]any{
					"type": "string",
					"enum": []string{discussProbeActSend, discussProbeActNoAction},
					"description": "\"" + discussProbeActSend + "\" when the bot should act this wake-up and that action must include at least one message; " +
						"\"" + discussProbeActNoAction + "\" when the bot should stay silent and its wake-up should not run.",
				},
				"reason": map[string]any{
					"type": "string",
					"description": "Brief justification. When should_act is \"" + discussProbeActSend +
						"\" this is forwarded to the bot as advisory context for what to say and what to do first.",
				},
			},
			"required": []string{"should_act", "reason"},
		},
	}
}

// extractDiscussProbeDecision reads the decide call out of a probe response.
// A missing or unparseable decision is reported as such rather than coerced,
// so the caller can fail closed and record which failure it was.
func extractDiscussProbeDecision(toolCalls []sdk.ToolCall) (shouldAct, reason, outcome string) {
	for _, call := range toolCalls {
		if !strings.EqualFold(strings.TrimSpace(call.ToolName), discussProbeToolName) {
			continue
		}
		raw, err := json.Marshal(call.Input)
		if err != nil {
			return "", "", discussProbeOutcomeMalformed
		}
		// Pointers so a missing key and an explicit null are both distinguishable
		// from the empty string, and a wrong type fails the unmarshal. The tool
		// schema marks both fields required; accepting a half-filled decision
		// would mean trusting a judge that did not answer the question asked.
		var decoded struct {
			ShouldAct *string `json:"should_act"`
			Reason    *string `json:"reason"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return "", "", discussProbeOutcomeMalformed
		}
		if decoded.ShouldAct == nil || decoded.Reason == nil {
			reason := ""
			if decoded.Reason != nil {
				reason = strings.TrimSpace(*decoded.Reason)
			}
			return "", reason, discussProbeOutcomeMalformed
		}
		reason := strings.TrimSpace(*decoded.Reason)
		switch strings.TrimSpace(*decoded.ShouldAct) {
		case discussProbeActSend:
			return discussProbeActSend, reason, discussProbeOutcomeAct
		case discussProbeActNoAction:
			return discussProbeActNoAction, reason, discussProbeOutcomeNoAction
		default:
			return "", reason, discussProbeOutcomeMalformed
		}
	}
	return "", "", discussProbeOutcomeMissing
}

// runDiscussProbe judges whether this discuss wake-up should wake the primary.
//
// Fail-closed is the whole point of the gate: a missing model, an unreachable
// provider, an overflowing context, or a decision the judge never emitted all
// resolve to "do not act". The one exception is a gate that was never
// configured, which returns Ran=false and leaves the caller's behavior
// untouched.
func (s *Service) runDiscussProbe(ctx context.Context, cmd turn.StartTurnCommand, resolved ResolveRunConfigResult) discussProbeResult {
	// Private conversations are a different contract and are deliberately not
	// gated. Chat mode delivers the model's text straight to the user, so there
	// is no message tool to require and no "should I interject?" question to
	// judge — every message in a DM is addressed to the bot by construction.
	// The gate exists for group chatter, where the bot is an observer.
	//
	// Note that an unset conversation type normalizes to private, so it also
	// bypasses. That is the repo-wide convention (the same predicate decides
	// `DiscussAddressed`), and erring toward the ungated contract keeps an
	// unclassified conversation answerable rather than silently mute.
	if turn.IsPrivateConversationType(cmd.ConversationType) {
		return discussProbeResult{}
	}

	// A bot-level override is a recorded operator decision that this chat is
	// gated. From here on, "we could not read the configuration" must never
	// resolve to "there is no gate" — that would let a transient database error
	// hand the turn a free pass through the very check the operator asked for.
	gateConfigured := strings.TrimSpace(resolved.DiscussProbeModelID) != ""

	probeModelID, ownerUserID, err := s.resolveDiscussProbeModel(ctx, cmd.BotID, resolved.DiscussProbeModelID)
	if err != nil {
		if gateConfigured {
			return s.reportDiscussProbe(cmd, resolved.DiscussProbeModelID,
				discussProbeFailedClosed(), "config_unreadable", err)
		}
		// No override: the only thing that failed is the lookup that would have
		// told us whether a fallback exists. Failing closed here would mute every
		// bot without a gate — including the permanent case of a bot with no
		// owner — so an unconfigured chat stays unconfigured.
		return s.reportDiscussProbe(cmd, "", discussProbeResult{}, "fallback_lookup_failed", err)
	}
	if probeModelID == "" {
		return s.reportDiscussProbe(cmd, "", discussProbeResult{}, discussProbeCauseNotConfigured, nil)
	}

	probeModel, provider, err := s.fetchChatModel(ctx, probeModelID)
	if err != nil {
		return s.reportDiscussProbe(cmd, probeModelID, discussProbeFailedClosed(), "model_unresolvable", err)
	}
	if !discussProbeModelCanJudge(probeModel) {
		if gateConfigured {
			// The operator named this model for this job. That it cannot do the
			// job is a configuration error to surface, not permission to skip
			// the check they asked for — the same rule as an unreadable config.
			return s.reportDiscussProbe(cmd, probeModelID, discussProbeFailedClosed(),
				"configured_model_cannot_call_tools", nil)
		}
		// Inherited: nobody chose this model for judging, and the title model is
		// only validated as type=chat. Leaving the chat ungated is the stated
		// policy for a fallback that cannot judge.
		return s.reportDiscussProbe(cmd, probeModelID, discussProbeResult{},
			"inherited_model_cannot_call_tools", nil)
	}

	budget, ok := discussProbeContextBudget(probeModel.Config.ContextBudgetMaxTokens())
	if !ok {
		// The judge's window cannot hold a minimal request. That is a
		// configuration fault, reported as such instead of discovered from a
		// provider rejection on every wake-up.
		return s.reportDiscussProbe(cmd, probeModelID, discussProbeFailedClosed(), "model_window_too_small", nil)
	}
	messages := discussMessagesToSDK(admitDiscussProbeMessages(cmd.DiscussMessages, budget))
	if len(messages) == 0 {
		return s.reportDiscussProbe(cmd, probeModelID, discussProbeFailedClosed(), "no_valid_window", nil)
	}

	// The run config already carries the bot identity; re-reading the row here
	// would be a second query for a value we were handed.
	botInfo := resolved.RunConfig.Bot
	system := native.GenerateDiscussProbePrompt(botInfo, botInfo.Timezone)

	authService := providers.NewService(nil, s.queries, "")
	authCtx := oauthctx.WithUserID(ctx, ownerUserID)
	creds, err := authService.ResolveModelCredentials(authCtx, provider)
	if err != nil {
		return s.reportDiscussProbe(cmd, probeModelID, discussProbeFailedClosed(), "credentials_unresolvable", err)
	}

	sdkModel := models.NewSDKChatModel(models.SDKModelConfig{
		ModelID:               probeModel.ModelID,
		ClientType:            provider.ClientType,
		APIKey:                creds.APIKey,
		CodexAccountID:        creds.CodexAccountID,
		BaseURL:               providers.ProviderConfigString(provider, "base_url"),
		ChatCompletionsCompat: providers.ProviderConfigString(provider, models.ChatCompletionsCompatConfigKey),
	})

	probeCtx, cancel := context.WithTimeout(ctx, discussProbeTimeout)
	defer cancel()

	tools := []sdk.Tool{discussProbeTool()}
	cacheTTL := providers.ProviderConfigString(provider, "prompt_cache_ttl")
	cachedSystem, cachedMessages, cachedTools := models.ApplyPromptCache(sdkModel, cacheTTL, system, messages, tools)

	generated, err := sdk.NewClient().GenerateTextResult(probeCtx,
		discussProbeGenerateOptions(sdkModel, cachedSystem, cachedMessages, cachedTools)...)
	if err != nil {
		return s.reportDiscussProbe(cmd, probeModelID, discussProbeFailedClosed(), "model_call_failed", err)
	}

	shouldAct, reason, outcome := extractDiscussProbeDecision(generated.ToolCalls)
	return s.reportDiscussProbe(cmd, probeModelID, discussProbeResult{
		Ran:       true,
		Activated: shouldAct == discussProbeActSend,
		Reason:    reason,
		Outcome:   outcome,
	}, "judged", nil)
}

// discussProbeFailedClosed is the verdict for a gate that was configured but
// could not reach a judgement. Named rather than inlined so every such exit is
// visibly the same decision: the gate ran, and it stays shut.
func discussProbeFailedClosed() discussProbeResult {
	return discussProbeResult{Ran: true, Outcome: discussProbeOutcomeError}
}

// reportDiscussProbe emits one line per gate exit and returns the verdict
// unchanged, so every return from the gate is also a record of it.
//
// The field set is identical on every path on purpose. This log is the only
// account of why a bot did or did not speak, and the question it has to answer
// — "was that a judgement, a broken config, or an outage?" — is unanswerable if
// each branch logs a different shape. `cause` distinguishes the branches;
// `gate_ran` separates "the gate held it shut" from "there was no gate".
func (s *Service) reportDiscussProbe(
	cmd turn.StartTurnCommand,
	modelID string,
	result discussProbeResult,
	cause string,
	err error,
) discussProbeResult {
	if s.logger == nil {
		return result
	}
	outcome := result.Outcome
	if !result.Ran {
		outcome = "disabled"
	}
	fields := []any{
		slog.String("bot_id", cmd.BotID),
		slog.String("session_id", cmd.ThreadID),
		slog.String("model_id", modelID),
		slog.String("cause", cause),
		slog.String("outcome", outcome),
		slog.Bool("gate_ran", result.Ran),
		slog.Bool("activated", result.Activated),
	}
	if result.Reason != "" {
		fields = append(fields, slog.String("reason", result.Reason))
	}
	if err != nil {
		fields = append(fields, slog.Any("error", err))
	}
	// Level is chosen by cause, not by whether the gate ran. Keying off Ran
	// buried the abnormal shutdowns — a model that cannot call tools disables
	// the gate with no error attached, so it logged at DEBUG and vanished from
	// a default INFO deployment, which is precisely the case an operator needs
	// to see.
	switch {
	case cause == discussProbeCauseNotConfigured:
		// A chat with no gate configured is the default state, not an event.
		s.logger.Debug("discuss probe", fields...)
	case err != nil || !result.Ran ||
		result.Outcome == discussProbeOutcomeError ||
		result.Outcome == discussProbeOutcomeMissing ||
		result.Outcome == discussProbeOutcomeMalformed:
		s.logger.Warn("discuss probe", fields...)
	default:
		s.logger.Info("discuss probe", fields...)
	}
	return result
}

// appendDiscussActivation carries the probe's activation contract into the
// primary turn as the last user message.
//
// Tail placement is the point. The system prompt is where a standing rule would
// normally live, but this one has to survive a long history: a requirement
// stated once, thousands of tokens back, is exactly what a model drops. Sitting
// adjacent to the generation point, it cannot be crowded out.
//
// It must be added to BOTH representations. The provider context compiler
// (contextview.ProviderRunConfigApplier) rebuilds Messages from
// ContextSourceFrags whenever fragments are present, so a contract that lives
// only in the message slice is silently discarded before the request is built —
// the gate would open and the primary would still run under the "you may stay
// silent" contract.
//
// Callers must inject inline images before calling this. The activation is
// itself a trailing user message, and both image injectors scan backwards for
// the last user message, so appending first would staple fresh vision input
// onto the instruction instead of the chat message that carried it.
//
// A gate that did not run, or ran and declined, appends nothing — the caller
// never reaches here on a decline, and a disabled gate must leave the turn
// byte-identical to what it was before this feature existed.
func appendDiscussActivation(
	messages []sdk.Message,
	frags []contextfrag.ContextFrag,
	probe discussProbeResult,
	scope contextfrag.Scope,
) ([]sdk.Message, []contextfrag.ContextFrag) {
	if !probe.Ran || !probe.Activated {
		return messages, frags
	}
	message := sdk.UserMessage(native.GenerateDiscussActivationPrompt(probe.Reason))
	return append(messages, message), append(frags, discussActivationFrag(message, len(frags), scope))
}

// discussActivationFrag is the fragment form of the activation contract.
//
// OverflowKeep is not an optimization: the contract is the entire reason the
// primary was woken, so trimming it under budget pressure would produce the
// worst outcome — paying for the judge and then running the turn without the
// instruction the judge's verdict authorized.
//
// TrustSystem, because this is the runtime speaking, not a participant. Nothing
// in the conversation may impersonate it.
func discussActivationFrag(message sdk.Message, index int, scope contextfrag.Scope) contextfrag.ContextFrag {
	return contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID:      "discuss.probe.activation",
		Message: message,
		Kind:    contextfrag.KindRuntimeContext,
		Slot:    contextfrag.SlotHistory,
		// Sorted after every composed message so the contract stays last.
		Index:    index,
		Priority: contextfrag.PriorityForMessage(message),
		// The reason changes every wake-up; caching it would be a cache miss
		// dressed as a hit.
		CacheClass: contextfrag.CacheNever,
		Trust:      contextfrag.TrustSystem,
		Scope:      scope,
		Source:     "discuss_probe",
		SourceID:   "activation",
		Collector:  "discuss_probe",
		Budget:     contextfrag.BudgetPolicy{Overflow: contextfrag.OverflowKeep},
	})
}
