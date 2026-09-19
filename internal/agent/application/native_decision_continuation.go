package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/models"
)

// runNativeDecisionContinuation owns the model stream after a committed answer.
// Authorization and answer/tool-result persistence remain with each decision
// kind; cancellation, step persistence and terminal publication share one path.
func (s *Service) runNativeDecisionContinuation(ctx context.Context, req ChatRequest, cfg native.RunConfig, modelID string, runtimeLifecycle *continuationLifecycleResult, eventCh chan<- WSStreamEvent) error {
	terminal := s.contextLifecycleTerminal(ctx, cfg)
	var lifecycleCause error
	var lifecycleDeferred bool
	var terminalEventSeen bool
	defer func() {
		if runtimeLifecycle != nil {
			runtimeLifecycle.cause = lifecycleCause
			runtimeLifecycle.deferred = lifecycleDeferred
			if snapshot, ok := cfg.ContextLifecycle.Snapshot(); ok {
				runtimeLifecycle.snapshot = &snapshot
			}
			return
		}
		if !lifecycleDeferred {
			terminal(lifecycleCause)
		}
	}()

	continuationRC := resolvedContext{runConfig: cfg, model: models.GetResponse{ID: modelID}}
	stepCommitter, err := s.bindQueueContinuation(ctx, &req, &cfg, continuationRC)
	if err != nil {
		return err
	}
	reasoningTiming := newReasoningTimingTracker(nil)
	configureNativeReasoningTiming(&cfg, reasoningTiming, stepCommitter)
	idleCtx, idleCancel := s.withStreamIdleTimeout(ctx, reasoningEffortForIdle(cfg))
	defer idleCancel.Stop()
	stream := s.agent.Stream(idleCtx, cfg)
	stored := false
	failureEventForwarded := false
	var hasVisibleOutput bool
	for event := range stream {
		idleCancel.Reset()
		if event.Type == native.EventToolCallStart {
			idleCancel.RecordToolCall()
		}
		if eventErr := agentStreamLifecycleError(event); eventErr != nil && lifecycleCause == nil {
			lifecycleCause = eventErr
			// The public event forwarded downstream carries only a stable code;
			// keep the runtime's private detail in the server log so a failed
			// continuation can be diagnosed.
			s.logContinuationStreamError(req.RunID, event)
		}
		if event.IsTerminal() {
			terminalEventSeen = true
			lifecycleDeferred = pendingContinuationDecision(event)
			if !lifecycleDeferred {
				switch event.Type {
				case native.EventAgentEnd:
					lifecycleCause = nil
				case native.EventAgentAbort:
					if idleCancel.DidFire() {
						lifecycleCause = context.Cause(idleCtx)
					} else if context.Cause(ctx) != nil || lifecycleCause == nil {
						lifecycleCause = agentAbortCause(ctx)
					}
				}
			}
		}
		if hasVisibleAgentStreamOutput(event) {
			hasVisibleOutput = true
		}
		if event.Type == native.EventAgentAbort && idleCancel.DidFire() && eventCh != nil {
			if failureData, marshalErr := json.Marshal(agentFailureStreamEvent(context.Cause(idleCtx))); marshalErr == nil {
				select {
				case eventCh <- json.RawMessage(failureData):
					failureEventForwarded = true
				case <-ctx.Done():
					lifecycleCause = context.Cause(ctx)
					return lifecycleCause
				}
			}
		}
		data, err := json.Marshal(publicAgentStreamEvent(event))
		if err != nil {
			continue
		}
		if !stored && event.IsTerminal() && len(event.Messages) > 0 {
			if snap, ok := extractTerminalSnapshot(data); ok {
				if stepCommitter == nil {
					snap.reasoningTiming = takeTerminalReasoningTiming(reasoningTiming, event.Type)
				}
				snap.visibleOutput = hasVisibleOutput
				snap.failureCode = snapshotFailureCode(idleCancel.DidFire(), lifecycleCause)
				lifecycleDeferred = lifecycleDeferred || snap.deferredToolID != ""
				if snap.aborted && !lifecycleDeferred && lifecycleCause == nil {
					lifecycleCause = agentAbortCause(ctx)
				}
				var storeErr error
				if stepCommitter != nil {
					storeErr = stepCommitter.finish(ctx, extractInputTokensFromUsage(snap.usage))
				} else {
					storeErr = s.persistTerminalSnapshot(
						context.WithoutCancel(ctx),
						req,
						resolvedContext{runConfig: cfg, model: models.GetResponse{ID: modelID}},
						snap,
					)
				}
				if storeErr != nil {
					lifecycleCause = storeErr
					lifecycleDeferred = false
					return storeErr
				}
				stored = true
			}
		}
		if eventCh != nil && shouldForwardAfterIdleFailure(event, failureEventForwarded) {
			select {
			case eventCh <- json.RawMessage(data):
			case <-ctx.Done():
				lifecycleCause = context.Cause(ctx)
				return lifecycleCause
			}
		}
	}
	if !stored && stepCommitter != nil {
		if storeErr := stepCommitter.finish(ctx, 0); storeErr != nil {
			lifecycleCause = storeErr
			return storeErr
		}
		stored = true
	}
	if stepCommitter != nil {
		if commitErr := stepCommitter.err(); commitErr != nil && ctx.Err() == nil {
			lifecycleCause = commitErr
			return commitErr
		}
	}
	if idleCancel.DidFire() {
		lifecycleCause = context.Cause(idleCtx)
		if !stored {
			if _, storeErr := s.persistTurnFailure(context.WithoutCancel(ctx), req, resolvedContext{runConfig: cfg, model: models.GetResponse{ID: modelID}}, snapshotFailureCode(true, lifecycleCause)); storeErr != nil {
				s.logger.ErrorContext(ctx, "decision continuation timeout persist failed", slog.Any("error", storeErr))
			}
		}
		if eventCh != nil && !failureEventForwarded {
			if data, marshalErr := json.Marshal(agentFailureStreamEvent(lifecycleCause)); marshalErr == nil {
				select {
				case eventCh <- json.RawMessage(data):
				case <-ctx.Done():
				}
			}
		}
		return lifecycleCause
	}
	if ctx.Err() != nil {
		lifecycleCause = context.Cause(ctx)
		return lifecycleCause
	}
	if lifecycleCause == nil && !lifecycleDeferred && !terminalEventSeen {
		lifecycleCause = errors.New("agent continuation ended without a terminal event")
	}
	return nil
}
