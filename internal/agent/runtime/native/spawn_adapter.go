package native

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	tools "github.com/felinics/memoh/internal/agent/tool"
)

// SpawnStepCommitFactory builds the per-step persistence callback for one
// spawned run. It receives the run context (which carries the admitted run's
// identity) and returns nil when incremental persistence is unavailable, in
// which case the spawn provider keeps its terminal-snapshot persistence.
// turnRequestMessageID is the persisted task user message the step rows bind
// to, so every step lands in the turn admission allocated.
type SpawnStepCommitFactory func(
	ctx context.Context,
	botID, sessionID, modelUUID, turnRequestMessageID string,
	contextLifecycle *contextfrag.LifecycleHolder,
	onPersisted func(),
) (
	func(context.Context, int, *sdk.StepResult) error,
	func(context.Context, int, *sdk.StepResult) error,
)

// SpawnRunObservation carries the terminal outcome selected by the session
// runtime that serialized terminal publication against routed controls.
type SpawnRunObservation struct {
	TerminalResolved bool
	TerminalOutcome  tools.SpawnAttemptDisposition
}

// SpawnRunObserver publishes one spawned-run event and reports a terminal
// outcome when the session runtime resolved one. Nonterminal events and
// observers without a session runtime return the zero observation.
type SpawnRunObserver func(StreamEvent) SpawnRunObservation

// SpawnRunObserverFactory builds the per-event publisher for one spawned run.
// A nil return means nothing observes this run and events are not forwarded.
type SpawnRunObserverFactory func(ctx context.Context) SpawnRunObserver

var errSpawnAgentAborted = errors.New("agent run aborted")

// SpawnAdapter wraps *Agent to satisfy tools.SpawnAgent without creating
// an import cycle (tools -> agent).
type SpawnAdapter struct {
	agent       *Agent
	stepCommit  SpawnStepCommitFactory
	runObserver SpawnRunObserverFactory
}

// NewSpawnAdapter creates a SpawnAdapter from the given Agent.
func NewSpawnAdapter(a *Agent) *SpawnAdapter {
	return &SpawnAdapter{agent: a}
}

// SetStepCommitFactory installs incremental step persistence for spawned runs.
func (s *SpawnAdapter) SetStepCommitFactory(f SpawnStepCommitFactory) {
	s.stepCommit = f
}

// SetRunObserverFactory installs live event publishing for spawned runs.
func (s *SpawnAdapter) SetRunObserverFactory(f SpawnRunObserverFactory) {
	s.runObserver = f
}

// installStepCommit resolves the step-commit callback for this run and wires
// it into the run config. It reports whether incremental persistence owns the
// run's history, so the caller can skip its terminal snapshot.
func (s *SpawnAdapter) installStepCommit(ctx context.Context, cfg tools.SpawnRunConfig, rc *RunConfig) bool {
	if s.stepCommit == nil {
		return false
	}
	commit, interrupt := s.stepCommit(
		ctx,
		cfg.Identity.BotID,
		cfg.Identity.SessionID,
		cfg.ModelUUID,
		cfg.TurnRequestMessageID,
		rc.ContextLifecycle,
		cfg.OnStepPersisted,
	)
	if commit == nil || interrupt == nil {
		return false
	}
	rc.OnStepCommitted = commit
	rc.OnStepInterrupted = interrupt
	return true
}

func (s *SpawnAdapter) Generate(ctx context.Context, cfg tools.SpawnRunConfig) (*tools.SpawnResult, error) {
	rc := runConfigFromSpawnRunConfig(cfg)
	persisted := s.installStepCommit(ctx, cfg, &rc)

	result, err := s.agent.Generate(ctx, rc)
	if err != nil {
		return spawnFailureResult(rc), err
	}

	spawnResult := &tools.SpawnResult{
		Messages:  result.Messages,
		Text:      result.Text,
		Usage:     result.Usage,
		Persisted: persisted,
	}
	if snapshot, ok := rc.ContextLifecycle.Snapshot(); ok {
		spawnResult.ContextLifecycle = &snapshot
	}
	return spawnResult, nil
}

func runConfigFromSpawnRunConfig(cfg tools.SpawnRunConfig) RunConfig {
	runID := strings.TrimSpace(cfg.RunID)
	if runID == "" {
		runID = uuid.NewString()
	}
	messages := cfg.Messages
	var currentUserMessageIndex *int
	if cfg.Query != "" {
		messages = append(messages, sdk.Message{
			Role:    sdk.MessageRoleUser,
			Content: []sdk.MessagePart{sdk.TextPart{Text: cfg.Query}},
		})
		index := len(messages) - 1
		currentUserMessageIndex = &index
	}

	identity := SessionContext{
		BotID:               cfg.Identity.BotID,
		ChatID:              cfg.Identity.ChatID,
		SessionID:           cfg.Identity.SessionID,
		UserID:              cfg.Identity.UserID,
		ChannelIdentityID:   cfg.Identity.ChannelIdentityID,
		CurrentPlatform:     cfg.Identity.CurrentPlatform,
		ReplyTarget:         cfg.Identity.ReplyTarget,
		ConversationType:    cfg.Identity.ConversationType,
		SessionToken:        cfg.Identity.SessionToken,
		WorkspaceTargetID:   cfg.Identity.WorkspaceTargetID,
		WorkspaceTargetKind: cfg.Identity.WorkspaceTargetKind,
		WorkspaceTargetName: cfg.Identity.WorkspaceTargetName,
		WorkdirPath:         cfg.Identity.WorkdirPath,
		TimezoneLocation:    cfg.Identity.TimezoneLocation,
		IsSubagent:          cfg.Identity.IsSubagent,
	}
	skills := make([]SkillEntry, 0, len(cfg.Skills))
	for name, skill := range cfg.Skills {
		skills = append(skills, SkillEntry{
			Name:        name,
			Description: skill.Description,
			Content:     skill.Content,
			Path:        skill.Path,
		})
	}
	rc := RunConfig{
		RunID:                          runID,
		Model:                          cfg.Model,
		CurrentModelUUID:               cfg.ModelUUID,
		CurrentModelID:                 cfg.ModelID,
		CurrentModelProvider:           cfg.ModelProvider,
		System:                         cfg.System,
		Query:                          cfg.Query,
		ContextQueryMaterialized:       cfg.Query != "",
		ContextCurrentUserMessageIndex: currentUserMessageIndex,
		SessionType:                    cfg.SessionType,
		Messages:                       messages,
		ReasoningConfig:                cfg.ReasoningConfig,
		ReasoningStoredEffort:          cfg.ReasoningStoredEffort,
		ReasoningRequestedEffort:       cfg.ReasoningRequestedEffort,
		PromptCacheTTL:                 cfg.PromptCacheTTL,
		ChatCompletionsCompat:          cfg.ChatCompletionsCompat,
		SupportsImageInput:             cfg.SupportsImageInput,
		SupportsFileInput:              cfg.SupportsFileInput,
		SupportsToolCall:               cfg.SupportsToolCall,
		Identity:                       identity,
		Skills:                         skills,
		BackgroundManager:              cfg.BackgroundManager,
		ContextBudgetMaxTokens:         cfg.ContextBudgetMaxTokens,
		ContextToolExchangePolicy:      cfg.ContextToolExchangePolicy,
		ContextScope: contextfrag.Scope{
			BotID:             identity.BotID,
			ChatID:            identity.ChatID,
			SessionID:         identity.SessionID,
			ChannelIdentityID: identity.ChannelIdentityID,
			Platform:          identity.CurrentPlatform,
		},
		LoopDetection: LoopDetectionConfig{
			Enabled: cfg.LoopDetection.Enabled,
		},
		ContextLifecycle: contextfrag.NewLifecycleHolder(),
	}
	rc.ContextSourceFrags = SpawnContextSourceFrags(rc)
	return rc
}

// SpawnContextSourceFrags uses typed system sections only when they reproduce
// the caller-supplied System exactly. Custom spawn prompts deliberately stay
// on the legacy collector fallback so PR1 cannot replace or normalize them.
func SpawnContextSourceFrags(rc RunConfig) []contextfrag.ContextFrag {
	sections := GenerateSystemSections(SystemPromptParams{SessionType: rc.SessionType})
	if renderSystemSections(sections) != rc.System {
		return nil
	}
	query := rc.Query
	if rc.ContextQueryMaterialized {
		query = ""
	}
	messages := contextfrag.CompileFrags(contextfrag.CompileInput{
		Scope:                   rc.ContextScope,
		Messages:                rc.Messages,
		CurrentUserMessageIndex: rc.ContextCurrentUserMessageIndex,
		Query:                   query,
	})
	frags := SystemSectionFrags(sections, rc.ContextScope)
	return append(frags, messages...)
}

// GenerateWithWatchdog runs the agent in streaming mode, touching the
// provided touchFn on every stream event (token, tool progress, etc.).
// It collects the full result and returns it in the same shape as Generate.
// This enables activity-based watchdog monitoring for subagent execution.
func (s *SpawnAdapter) GenerateWithWatchdog(ctx context.Context, cfg tools.SpawnRunConfig, touchFn func()) (*tools.SpawnResult, error) {
	rc := runConfigFromSpawnRunConfig(cfg)
	persisted := s.installStepCommit(ctx, cfg, &rc)
	var observe SpawnRunObserver
	if s.runObserver != nil {
		observe = s.runObserver(ctx)
	}

	// Use Stream instead of Generate to get per-token/per-tool activity signals.
	eventCh := s.agent.Stream(ctx, rc)

	var allText strings.Builder
	var finalMessages []sdk.Message
	var totalUsage sdk.Usage
	var lastError string
	completed := false
	var abortEvent *StreamEvent
	var endEvent *StreamEvent
	var pendingErrors []StreamEvent

	for evt := range eventCh {
		// Touch the watchdog on every event — this is the activity signal.
		touchFn()
		switch evt.Type {
		case EventError:
			// Error is attempt-local until the caller resolves the following abort:
			// a retry clears it, while owning cancellation must not be mislabeled as
			// a provider failure merely because the two raced.
			pendingErrors = append(pendingErrors, evt)
		case EventRetry:
			observeSpawnEvents(observe, pendingErrors)
			pendingErrors = nil
			observeSpawnEvent(observe, evt)
		case EventAgentAbort:
			// The spawn provider owns the outer retry loop, so it decides below
			// whether this is a terminal abort or only the end of one attempt.
		case EventAgentEnd:
			// A clean end is authoritative. Any un-retried transient error did not
			// become this attempt's outcome and must not poison the terminal view.
			pendingErrors = nil
		default:
			// Published before this loop reads anything out of the event, so a
			// subscriber to the spawned session never lags the parent's view.
			observeSpawnEvent(observe, evt)
		}

		switch evt.Type {
		case EventTextDelta:
			allText.WriteString(evt.Delta)
		case EventError:
			lastError = evt.Error
		case EventRetry:
			// The stream is retrying what it just reported, so the error is no
			// longer this run's outcome. Holding it would turn a later abort
			// into a failure report naming a provider error the run recovered
			// from; a retry that gives up publishes its own final error.
			lastError = ""
		case EventAgentEnd, EventAgentAbort:
			completed = evt.Type == EventAgentEnd
			if completed {
				terminal := evt
				endEvent = &terminal
			} else {
				terminal := evt
				abortEvent = &terminal
			}
			if evt.Messages != nil {
				_ = json.Unmarshal(evt.Messages, &finalMessages)
			}
			if evt.Usage != nil {
				_ = json.Unmarshal(evt.Usage, &totalUsage)
			}
		}
	}

	var runErr error
	// Check if context was cancelled (watchdog fired or parent cancelled).
	if !completed && ctx.Err() != nil {
		if cause := context.Cause(ctx); cause != nil {
			runErr = cause
		} else {
			runErr = ctx.Err()
		}
	}
	// A stream that errored without reaching a clean end is a failed attempt,
	// not a short answer. Surfacing the provider's own error text is what lets
	// the caller's retry patterns (429 / 5xx / connection reset) match; the
	// pre-fix behavior swallowed these into an empty success. An error that
	// the run recovered from (mid-stream retry reached EventAgentEnd) stays
	// invisible here, exactly like the main chat path.
	if runErr == nil && !completed {
		if lastError != "" {
			runErr = errors.New(lastError)
		} else {
			runErr = errSpawnAgentAborted
		}
	}
	if runErr != nil {
		outcome := observeSpawnAttemptFailure(ctx, observe, cfg, abortEvent, pendingErrors, lastError, runErr)
		if cfg.ReconcileTerminal != nil {
			cfg.ReconcileTerminal(outcome)
		}
		return spawnFailureResult(rc), runErr
	}
	disposition := tools.SpawnAttemptCompleted
	if cfg.ResolveCompletion != nil {
		disposition = cfg.ResolveCompletion()
	}
	terminal := StreamEvent{Type: EventAgentEnd}
	if endEvent != nil {
		terminal = *endEvent
	}
	if disposition != tools.SpawnAttemptCompleted {
		terminal.Type = EventAgentAbort
		if disposition != tools.SpawnAttemptAbort {
			observeSpawnEvent(observe, StreamEvent{Type: EventError, Error: errSpawnAgentAborted.Error()})
		}
	}
	observation := observeSpawnEvent(observe, terminal)
	if observation.TerminalResolved {
		disposition = observation.TerminalOutcome
		if cfg.ReconcileTerminal != nil {
			cfg.ReconcileTerminal(disposition)
		}
	}
	if disposition != tools.SpawnAttemptCompleted {
		cause := context.Cause(ctx)
		if cause == nil {
			if disposition == tools.SpawnAttemptAbort {
				cause = context.Canceled
			} else {
				cause = errSpawnAgentAborted
			}
		}
		return spawnFailureResult(rc), cause
	}

	spawnResult := &tools.SpawnResult{
		Messages:  finalMessages,
		Text:      allText.String(),
		Usage:     &totalUsage,
		Persisted: persisted,
	}
	if snapshot, ok := rc.ContextLifecycle.Snapshot(); ok {
		spawnResult.ContextLifecycle = &snapshot
	}
	return spawnResult, nil
}

func observeSpawnAttemptFailure(
	ctx context.Context,
	observe SpawnRunObserver,
	cfg tools.SpawnRunConfig,
	abortEvent *StreamEvent,
	pendingErrors []StreamEvent,
	lastError string,
	runErr error,
) tools.SpawnAttemptDisposition {
	disposition := tools.SpawnAttemptFailure
	if cfg.ResolveAttempt != nil {
		disposition = cfg.ResolveAttempt(runErr)
	} else if errors.Is(context.Cause(ctx), context.Canceled) {
		disposition = tools.SpawnAttemptAbort
	}
	if observe == nil {
		return disposition
	}
	reconcile := func(observation SpawnRunObservation) tools.SpawnAttemptDisposition {
		if observation.TerminalResolved {
			return observation.TerminalOutcome
		}
		return disposition
	}
	switch disposition {
	case tools.SpawnAttemptRetry:
		observeSpawnEvents(observe, pendingErrors)
		retryError := strings.TrimSpace(lastError)
		if retryError == "" {
			retryError = errSpawnAgentAborted.Error()
			observeSpawnEvent(observe, StreamEvent{Type: EventError, Error: retryError})
		}
		attempt := cfg.Attempt
		if attempt <= 0 {
			attempt = 1
		}
		maxAttempts := cfg.MaxAttempts
		if maxAttempts < attempt {
			maxAttempts = attempt
		}
		observeSpawnEvent(observe, StreamEvent{
			Type:       EventRetry,
			Attempt:    attempt,
			MaxAttempt: maxAttempts,
			RetryError: retryError,
		})
		return disposition
	case tools.SpawnAttemptAbort:
		if abortEvent != nil {
			return reconcile(observeSpawnEvent(observe, *abortEvent))
		}
		return disposition
	default:
		observeSpawnEvents(observe, pendingErrors)
		if len(pendingErrors) == 0 {
			observeSpawnEvent(observe, StreamEvent{Type: EventError, Error: errSpawnAgentAborted.Error()})
		}
		if abortEvent != nil {
			return reconcile(observeSpawnEvent(observe, *abortEvent))
		}
		return disposition
	}
}

func observeSpawnEvents(observe SpawnRunObserver, events []StreamEvent) {
	if observe == nil {
		return
	}
	for _, event := range events {
		observeSpawnEvent(observe, event)
	}
}

func observeSpawnEvent(observe SpawnRunObserver, event StreamEvent) SpawnRunObservation {
	if observe == nil {
		return SpawnRunObservation{}
	}
	return observe(event)
}

func spawnFailureResult(rc RunConfig) *tools.SpawnResult {
	if rc.ContextLifecycle == nil {
		return nil
	}
	snapshot, ok := rc.ContextLifecycle.Snapshot()
	if !ok {
		return nil
	}
	return &tools.SpawnResult{ContextLifecycle: &snapshot}
}

// SpawnSystemPrompt returns the system prompt for a given session type.
func SpawnSystemPrompt(sessionType string) string {
	return GenerateSystemPrompt(SystemPromptParams{
		SessionType: sessionType,
	})
}
