package native

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	tools "github.com/memohai/memoh/internal/agent/tool"
)

// SpawnStepCommitFactory builds the per-step persistence callback for one
// spawned run. It receives the run context (which carries the admitted run's
// identity) and returns nil when incremental persistence is unavailable, in
// which case the spawn provider keeps its terminal-snapshot persistence.
// turnRequestMessageID is the persisted task user message the step rows bind
// to, so every step lands in the turn admission allocated.
type SpawnStepCommitFactory func(ctx context.Context, botID, sessionID, modelUUID, turnRequestMessageID string, onPersisted func()) func(context.Context, int, *sdk.StepResult) error

// SpawnRunObserverFactory builds the per-event publisher for one spawned run.
// A nil return means nothing observes this run and events are not forwarded.
type SpawnRunObserverFactory func(ctx context.Context) func(StreamEvent)

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
	commit := s.stepCommit(ctx, cfg.Identity.BotID, cfg.Identity.SessionID, cfg.ModelUUID, cfg.TurnRequestMessageID, cfg.OnStepPersisted)
	if commit == nil {
		return false
	}
	rc.OnStepCommitted = commit
	return true
}

func (s *SpawnAdapter) Generate(ctx context.Context, cfg tools.SpawnRunConfig) (*tools.SpawnResult, error) {
	rc := runConfigFromSpawnRunConfig(cfg)
	persisted := s.installStepCommit(ctx, cfg, &rc)

	result, err := s.agent.Generate(ctx, rc)
	if err != nil {
		return nil, err
	}

	return &tools.SpawnResult{
		Messages:  result.Messages,
		Text:      result.Text,
		Usage:     result.Usage,
		Persisted: persisted,
	}, nil
}

func runConfigFromSpawnRunConfig(cfg tools.SpawnRunConfig) RunConfig {
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
		ReasoningEffort:                cfg.ReasoningEffort,
		PromptCacheTTL:                 cfg.PromptCacheTTL,
		ChatCompletionsCompat:          cfg.ChatCompletionsCompat,
		SupportsImageInput:             cfg.SupportsImageInput,
		SupportsFileInput:              cfg.SupportsFileInput,
		SupportsToolCall:               cfg.SupportsToolCall,
		Identity:                       identity,
		Skills:                         skills,
		BackgroundManager:              cfg.BackgroundManager,
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
	var observe func(StreamEvent)
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

	for evt := range eventCh {
		// Touch the watchdog on every event — this is the activity signal.
		touchFn()
		if observe != nil {
			// Published before this loop reads anything out of the event, so a
			// subscriber to the spawned session never lags the parent's view.
			observe(evt)
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
			if evt.Messages != nil {
				_ = json.Unmarshal(evt.Messages, &finalMessages)
			}
			if evt.Usage != nil {
				_ = json.Unmarshal(evt.Usage, &totalUsage)
			}
		}
	}

	// Check if context was cancelled (watchdog fired or parent cancelled).
	if ctx.Err() != nil {
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		return nil, ctx.Err()
	}

	// A stream that errored without reaching a clean end is a failed attempt,
	// not a short answer. Surfacing the provider's own error text is what lets
	// the caller's retry patterns (429 / 5xx / connection reset) match; the
	// pre-fix behavior swallowed these into an empty success. An error that
	// the run recovered from (mid-stream retry reached EventAgentEnd) stays
	// invisible here, exactly like the main chat path.
	if !completed && lastError != "" {
		return nil, errors.New(lastError)
	}

	return &tools.SpawnResult{
		Messages:  finalMessages,
		Text:      allText.String(),
		Usage:     &totalUsage,
		Persisted: persisted,
	}, nil
}

// SpawnSystemPrompt returns the system prompt for a given session type.
func SpawnSystemPrompt(sessionType string) string {
	return GenerateSystemPrompt(SystemPromptParams{
		SessionType: sessionType,
	})
}
