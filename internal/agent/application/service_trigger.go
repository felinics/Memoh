package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/schedule"
)

// attachCurrentTurnPrompt routes a trigger's rich prompt through Query so the
// context view classifies it as the live current request rather than history.
func attachCurrentTurnPrompt(cfg native.RunConfig, prompt string) native.RunConfig {
	cfg.Query = prompt
	return cfg
}

// TriggerSchedule executes a scheduled command via the internal agent.
func (s *Service) TriggerSchedule(ctx context.Context, botID string, payload schedule.TriggerPayload, token string) (triggerResult schedule.TriggerResult, err error) {
	if strings.TrimSpace(botID) == "" {
		return schedule.TriggerResult{}, errors.New("bot id is required")
	}
	if strings.TrimSpace(payload.Command) == "" {
		return schedule.TriggerResult{}, errors.New("schedule command is required")
	}

	submission, err := json.Marshal(scheduleSubmission{
		Kind:       "schedule",
		ScheduleID: strings.TrimSpace(payload.ID),
		Command:    payload.Command,
	})
	if err != nil {
		return schedule.TriggerResult{}, err
	}
	// Project the command as the run's request user turn so subscribers (an
	// open web session, any future thread subscriber) see what fired this run
	// while it is still executing — not only after it finishes. Text must equal
	// the persisted user message (prependTurnUserMessage uses req.Query =
	// payload.Command), otherwise the bubble's content would visibly change
	// when the runtime projection hands over to the database at run end.
	viewFn := func(handle sessionruntime.RunHandle) *sessionruntime.RunAdmissionView {
		return &sessionruntime.RunAdmissionView{
			RequestUserTurn: &chatview.UITurn{
				TurnID:    handle.TurnID,
				Role:      "user",
				Text:      payload.Command,
				Timestamp: time.Now(),
			},
		}
	}
	runCtx, admission, finish, err := s.admitTriggeredRun(ctx, botID, payload.SessionID, scheduleInvocationID(payload), submission, viewFn)
	if err != nil {
		// Including a busy answer: a fire that cannot take the thread's slot has
		// no value once the next one is due, so it is reported and dropped rather
		// than retried here.
		return schedule.TriggerResult{}, err
	}
	defer func() { finish(triggeredRunTerminal{cause: err}) }()
	ctx = runCtx

	// Runtime sessions (ACP, codex, claude-code) must never silently degrade
	// to the built-in model: resolve the driver and run the scheduled turn
	// through it.
	dispatch, err := s.resolveRuntimeDispatch(ctx, ChatRequest{BotID: botID, ThreadID: payload.SessionID})
	if err != nil {
		return schedule.TriggerResult{}, err
	}
	if dispatch.kind == dispatchExternal {
		return s.triggerScheduleRuntime(ctx, botID, payload, token, admission.RunID, dispatch.driver)
	}

	req := ChatRequest{
		BotID:           botID,
		ChatID:          botID,
		ThreadID:        payload.SessionID,
		RunID:           admission.RunID,
		Query:           payload.Command,
		UserID:          payload.OwnerUserID,
		Token:           token,
		Model:           payload.ModelID,
		ReasoningEffort: payload.ReasoningEffort,
		SessionType:     sessionmode.Schedule,
	}
	rc, req, err := s.resolve(ctx, req)
	if err != nil {
		return schedule.TriggerResult{}, err
	}
	req.RunID = rc.runConfig.RunID
	// The step committer refuses to arm without the durable turn identity
	// (step_commit.go), and that identity only exists after admission.
	req.TurnID = admission.TurnID
	req.TurnPosition = &admission.TurnPosition

	cfg := rc.runConfig
	cfg.SessionType = sessionmode.Schedule
	cfg.Identity.ChannelIdentityID = strings.TrimSpace(payload.OwnerUserID)
	cfg.ContextScope.ChannelIdentityID = strings.TrimSpace(payload.OwnerUserID)

	schedulePrompt := native.GenerateSchedulePrompt(native.Schedule{
		ID:          payload.ID,
		Name:        payload.Name,
		Description: payload.Description,
		Pattern:     payload.Pattern,
		MaxCalls:    payload.MaxCalls,
		Command:     payload.Command,
	})
	cfg = attachCurrentTurnPrompt(cfg, schedulePrompt)
	cfg = s.prepareRunConfig(ctx, cfg)
	terminal := s.contextLifecycleTerminal(ctx, cfg)
	var lifecycleCause error
	defer func() { terminal(lifecycleCause) }()

	// Wire the trigger run like an interactive turn: steps persist as they
	// complete, and every stream event is projected to the session runtime so
	// the run is visible while it executes. The previous Generate-based path
	// emitted nothing until storeRound wrote the whole round at the end.
	reasoningTiming := newReasoningTimingTracker(nil)
	stepCommitter := s.newAgentStepCommitter(ctx, req, rc)
	configureNativeReasoningTiming(&cfg, reasoningTiming, stepCommitter)

	result, streamErr := s.runTriggeredNativeStream(ctx, cfg, req, rc, admission.Handle, stepCommitter, reasoningTiming)
	lifecycleCause = streamErr
	return result, streamErr
}

func (s *Service) runTriggeredNativeStream(
	ctx context.Context,
	cfg native.RunConfig,
	req ChatRequest,
	rc resolvedContext,
	handle sessionruntime.RunHandle,
	stepCommitter *agentStepCommitter,
	reasoningTiming *reasoningTimingTracker,
) (schedule.TriggerResult, error) {
	// cancelStream remains the consumption brake: if the consumer stops early
	// (projection refused, terminal handled), cancelling unwinds the Stream
	// goroutine instead of leaving it blocked on an unread channel. The child
	// idle context independently owns first-event and between-event silence.
	streamCtx, cancelStream := context.WithCancel(ctx)
	idleCtx, idleCancel := s.withStreamIdleTimeout(streamCtx, reasoningEffortForIdle(cfg))
	defer func() {
		cancelStream()
		idleCancel.Stop()
	}()

	return s.consumeTriggeredStreamWithIdle(
		idleCtx,
		s.agent.Stream(idleCtx, cfg),
		req,
		rc,
		handle,
		stepCommitter,
		reasoningTiming,
		idleCancel,
	)
}

// consumeTriggeredStream drains a triggered (non-interactive) run's event
// stream. Every event is published to the session runtime so subscribers
// watch the run live; persistence follows the same discipline as the WS loop
// (streamChatWSResultWithHooks): steps persist as they complete via the step
// committer, with one terminal-snapshot write as the fallback when step
// persistence is unavailable, and an abort terminal is reported as an error
// outcome while its partial transcript still persists. What a trigger
// deliberately does NOT have is the WS client plumbing — no push channel or
// abort channel because there is no client. An application idle watchdog still
// bounds first-event and between-event silence, including manual schedule fires
// whose caller context has no deadline.
//
// The events channel is a parameter (rather than this function calling
// agent.Stream itself) so tests can drive the loop directly; the caller owns
// the stream context and must cancel it to unwind a still-running Stream
// goroutine when this returns early.
func (s *Service) consumeTriggeredStream(ctx context.Context, events <-chan native.StreamEvent, req ChatRequest, rc resolvedContext, handle sessionruntime.RunHandle, stepCommitter *agentStepCommitter, reasoningTiming *reasoningTimingTracker) (schedule.TriggerResult, error) {
	return s.consumeTriggeredStreamWithIdle(ctx, events, req, rc, handle, stepCommitter, reasoningTiming, nil)
}

func (s *Service) consumeTriggeredStreamWithIdle(ctx context.Context, events <-chan native.StreamEvent, req ChatRequest, rc resolvedContext, handle sessionruntime.RunHandle, stepCommitter *agentStepCommitter, reasoningTiming *reasoningTimingTracker, idle *idleCancel) (schedule.TriggerResult, error) {
	publishEvent := s.turnAgentEventPublisher(handle)
	if reasoningTiming == nil {
		reasoningTiming = newReasoningTimingTracker(nil)
	}

	var (
		lastSnapshot       terminalSnapshot
		hasSnapshot        bool
		terminalSeen       bool
		hasVisibleOutput   bool
		stored             bool
		terminalAborted    bool
		terminalPersistErr error
		streamErr          error
	)
	for event := range events {
		if event.IsTerminal() {
			terminalSeen = true
		}
		if idle != nil {
			idle.Reset()
			if event.Type == native.EventToolCallStart {
				idle.RecordToolCall()
			}
		}
		if eventErr := agentStreamEventError(event); eventErr != nil {
			s.logger.Error("triggered run stream error",
				slog.String("bot_id", req.BotID),
				slog.String("session_id", req.ThreadID),
				slog.Any("error", eventErr),
			)
			if streamErr == nil {
				streamErr = eventErr
			}
		}
		if event.IsTerminal() && event.Type == native.EventAgentAbort && strings.TrimSpace(event.ApprovalID) == "" {
			// A stopped run is not a success: mirror the WS loop, which maps
			// an abort terminal to agentAbortCause (a deferred approval is a
			// pending pause, not an abort, so it is excluded). The partial
			// transcript still persists below; only the reported outcome
			// changes — without this the schedule log would mark a stopped
			// run "ok", a regression from the Generate era, which errored.
			terminalAborted = true
			if context.Cause(ctx) != nil || streamErr == nil {
				streamErr = agentAbortCause(ctx)
			}
		}
		if hasVisibleAgentStreamOutput(event) {
			hasVisibleOutput = true
		}
		if publishEvent != nil && !event.IsTerminal() {
			if publishErr := publishEvent(ctx, publicAgentStreamEvent(event)); publishErr != nil {
				// A refused projection write (fence rejection, ownership
				// handoff) means this process may no longer own the run, and
				// continuing would persist history the runtime no longer
				// attributes to it — the same stop policy as the channel
				// pump (turn_service.go). Stop consuming; the caller's
				// stream-context cancel unwinds the producer goroutine.
				if streamErr == nil {
					streamErr = publishErr
				}
				break
			}
		}
		if event.IsTerminal() && len(event.Messages) > 0 {
			data, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				continue
			}
			snap, ok := extractTerminalSnapshot(data)
			if !ok {
				continue
			}
			if stepCommitter == nil {
				snap.reasoningTiming = takeTerminalReasoningTiming(reasoningTiming, event.Type)
			}
			snap.visibleOutput = hasVisibleOutput
			snap.failureCode = snapshotFailureCode(idle != nil && idle.DidFire(), streamErr)
			lastSnapshot = snap
			hasSnapshot = true
			if !stored && !runOwnershipLost(ctx) {
				if stepCommitter != nil {
					if storeErr := stepCommitter.finish(ctx, extractInputTokensFromUsage(snap.usage)); storeErr != nil {
						terminalPersistErr = runtimeHistoryError(storeErr)
						if streamErr == nil {
							streamErr = terminalPersistErr
						}
						s.logger.Error("triggered run step finalization failed", slog.Any("error", storeErr))
					} else {
						stored = true
					}
				} else {
					if storeErr := s.persistTerminalSnapshot(context.WithoutCancel(ctx), req, rc, snap); storeErr != nil {
						terminalPersistErr = runtimeHistoryError(storeErr)
						if streamErr == nil {
							streamErr = terminalPersistErr
						}
						s.logger.Error("triggered run terminal persist failed", slog.Any("error", storeErr))
					} else {
						stored = true
					}
				}
			}
		}
		if event.IsTerminal() && !stored && !runOwnershipLost(ctx) && terminalPersistErr == nil {
			switch {
			case !hasVisibleOutput:
				// A terminal event before visible assistant output has no output
				// row to persist; the admitted user message is already durable.
				stored = true
			case stepCommitter != nil:
				if storeErr := stepCommitter.finish(ctx, rc.estimatedTokens); storeErr != nil {
					terminalPersistErr = runtimeHistoryError(storeErr)
				} else {
					stored = true
				}
			default:
				terminalPersistErr = runtimeHistoryError(errors.New("agent terminal event has no persistable snapshot"))
			}
			if terminalPersistErr != nil && streamErr == nil {
				streamErr = terminalPersistErr
			}
		}
		if event.IsTerminal() && terminalPersistErr == nil && !runOwnershipLost(ctx) && publishEvent != nil {
			// The durable history write is the terminal proposal barrier. A crash
			// after this publication can now be completed by the session reaper;
			// publishing before it would make an uncommitted answer look complete.
			if publishErr := publishEvent(context.WithoutCancel(ctx), publicAgentStreamEvent(event)); publishErr != nil {
				if streamErr == nil {
					streamErr = publishErr
				}
				break
			}
		}
	}
	if streamErr == nil {
		streamErr = context.Cause(ctx)
	}

	// Mid-run abort/error: finalize whatever the step committer already landed
	// so the partial transcript survives for audit. This is a deliberate
	// behavior change from the Generate era, which persisted nothing on
	// failure — the partial record is the more honest one.
	if !stored && stepCommitter != nil && !runOwnershipLost(ctx) {
		if storeErr := stepCommitter.finish(ctx, rc.estimatedTokens); storeErr != nil {
			if streamErr == nil {
				streamErr = storeErr
			}
			s.logger.Error("triggered run step finalization failed", slog.Any("error", storeErr))
		}
	}

	switch {
	case runOwnershipLost(ctx):
		// The reaper names this run's outcome; a superseded owner must not
		// report a result for it (SR-DUR-002).
		return schedule.TriggerResult{}, sessionruntime.ErrRunOwnershipLost
	case terminalPersistErr != nil:
		return schedule.TriggerResult{}, terminalPersistErr
	case terminalAborted:
		// The transcript already persisted above; report the abort as the
		// outcome so the schedule log records the stop, not a success.
		return schedule.TriggerResult{}, streamErr
	case streamErr != nil && !hasSnapshot:
		if idle != nil && idle.DidFire() {
			if _, storeErr := s.persistTurnFailure(context.WithoutCancel(ctx), req, rc, snapshotFailureCode(true, streamErr)); storeErr != nil {
				s.logger.Error("triggered run timeout persist failed", slog.Any("error", storeErr))
			}
		}
		return schedule.TriggerResult{}, streamErr
	case !terminalSeen:
		return schedule.TriggerResult{}, errors.New("schedule run ended without a terminal event")
	}

	// A terminal snapshot settles the run even if a non-fatal stream error was
	// seen earlier (already logged); the trigger result mirrors the Generate
	// era's contract: final assistant text plus usage for the schedule log.
	text := ""
	if modelMsgs := sdkMessagesToModelMessages(lastSnapshot.sdkMessages); len(modelMsgs) > 0 {
		if idx := lastAssistantMessageIndex(modelMsgs); idx >= 0 {
			text = strings.TrimSpace(modelMsgs[idx].TextContent())
		}
	}
	return schedule.TriggerResult{
		Status:     "ok",
		Text:       text,
		UsageBytes: lastSnapshot.usage,
		ModelID:    rc.model.ID,
	}, nil
}

// scheduleSubmission is the canonical fingerprint input for a triggered turn.
// It carries what the trigger asked for and nothing
// about when it ran, so re-running one tick's work is recognized as the same
// submission rather than a new one.
type scheduleSubmission struct {
	Kind       string `json:"kind"`
	ScheduleID string `json:"schedule_id"`
	Command    string `json:"command"`
}

// scheduleInvocationID names one fire.
//
// Each fire runs in a thread of its own, and invocation uniqueness is already
// scoped per thread, so the thread id is what distinguishes consecutive fires.
// Naming it explicitly also keeps these ids correct if a schedule ever reuses one
// thread across fires, which would otherwise make every fire after the first look
// like a replay of the first.
func scheduleInvocationID(payload schedule.TriggerPayload) string {
	return "schedule:" + strings.TrimSpace(payload.ID) + ":" + strings.TrimSpace(payload.SessionID)
}
