package application

import (
	"context"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/chat/contextepoch"
	"github.com/felinics/memoh/internal/contextview"
)

// wireEpochStability connects the Context Epoch's pinned stability guards
// (spec rules 1 and 4) to the run config:
//
//   - History budget: a longer current request may never shrink the history
//     budget within an epoch. The first computed budget of the epoch is
//     recorded through the guarded pin write; later turns reuse the pinned
//     value and the selector's per-turn feedback no longer applies to history.
//   - Stable-prefix hash: the first observed hash of the epoch is pinned;
//     every later turn of the same epoch must reproduce it byte for byte, and
//     a mismatch raises the drift alarm instead of silently rewriting the
//     stable prefix.
func (s *Service) wireEpochStability(ctx context.Context, sessionID string, cfg *native.RunConfig) {
	if s == nil || s.contextEpochs == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	state, err := s.contextEpochs.Load(ctx, sessionID)
	if err != nil {
		// Degrade to the computed budget: a transient metadata read failure
		// must not fail the turn, and the pin writes retry next turn.
		s.logger.WarnContext(ctx, "context_epoch_load_failed",
			slog.String("path", "epoch_stability"),
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return
	}
	// A model switch rewrites the rendered system region, so it must turn the
	// page (spec: 显式翻篇) BEFORE this turn renders — otherwise the new
	// prefix would render under the old epoch and trip the drift alarm as an
	// unexplained change. The advance resets the epoch pins, which this turn
	// then re-observes and re-pins.
	state = s.bookModelChange(ctx, sessionID, cfg, state)
	if state.BudgetTokens > 0 {
		cfg.PinnedHistoryBudgetTokens = int(state.BudgetTokens)
	}
	cfg.RecordHistoryBudgetPin = func(pinCtx context.Context, tokens int) {
		if _, err := s.contextEpochs.PinBudget(pinCtx, sessionID, int64(tokens)); err != nil {
			s.logger.WarnContext(pinCtx, "context_epoch_budget_pin_failed",
				slog.String("session_id", sessionID),
				slog.Int("budget_tokens", tokens),
				slog.Any("error", err))
		}
	}
	cfg.RecordStablePrefixHash = func(reportCtx context.Context, observed string) {
		s.reportStablePrefixHash(reportCtx, sessionID, observed)
	}
}

// bookModelChange compares the epoch's pinned model identity with the model
// this turn renders with. A mismatch books a model-change advance and
// re-pins the identity for the new epoch; a missing identity pins the first
// observation. The (possibly advanced) state is returned so the caller wires
// the remaining pins against the current epoch.
func (s *Service) bookModelChange(ctx context.Context, sessionID string, cfg *native.RunConfig, state contextepoch.State) contextepoch.State {
	identity := contextview.StableModelIdentity(*cfg)
	if identity == "" {
		return state
	}
	if state.ModelIdentity == "" {
		pinned, err := s.contextEpochs.PinModelIdentity(ctx, sessionID, identity)
		if err != nil {
			s.logger.WarnContext(ctx, "context_epoch_model_identity_pin_failed",
				slog.String("session_id", sessionID),
				slog.Any("error", err))
			return state
		}
		return pinned
	}
	if state.ModelIdentity == identity {
		return state
	}
	advanced, err := s.contextEpochs.AdvanceContextEpoch(ctx, sessionID, "model-change", contextepoch.ReasonModelChange)
	if err != nil {
		// The turn still renders with the new model: the drift alarm reports
		// the unexplained prefix change, and the advance retries next turn.
		s.logger.WarnContext(ctx, "context_epoch_model_change_advance_failed",
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return state
	}
	pinned, err := s.contextEpochs.PinModelIdentity(ctx, sessionID, identity)
	if err != nil {
		s.logger.WarnContext(ctx, "context_epoch_model_identity_pin_failed",
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return advanced
	}
	return pinned
}

// reportStablePrefixHash applies the rule-1 accounting for one rendered
// turn. The state is re-read at report time — not captured at assembly —
// because an in-turn budget recovery may have advanced the epoch: comparing
// and attributing against the pre-recovery epoch would log drift under the
// wrong epoch (and could pin onto a state the recovery already replaced).
func (s *Service) reportStablePrefixHash(ctx context.Context, sessionID, observed string) {
	if observed == "" {
		return
	}
	state, err := s.contextEpochs.Load(ctx, sessionID)
	if err != nil {
		s.logger.WarnContext(ctx, "context_epoch_load_failed",
			slog.String("path", "prefix_hash_report"),
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return
	}
	pinned := state.PrefixHash
	if pinned == "" {
		stored, err := s.contextEpochs.PinPrefixHash(ctx, sessionID, observed)
		if err != nil {
			s.logger.WarnContext(ctx, "context_epoch_prefix_hash_pin_failed",
				slog.String("session_id", sessionID),
				slog.Any("error", err))
			return
		}
		pinned = stored.PrefixHash
		state = stored
	}
	if pinned == "" || pinned == observed {
		return
	}
	recordPrefixDrift(ctx)
	s.logger.ErrorContext(ctx, "context_epoch_prefix_drift",
		slog.String("session_id", sessionID),
		slog.Int64("epoch", state.Epoch),
		slog.String("pinned_prefix_hash", pinned),
		slog.String("observed_prefix_hash", observed))
}

// recordPrefixDrift counts rule-1 violations for the monitoring alarm: the
// stable prefix hash changed inside one epoch.
func recordPrefixDrift(ctx context.Context) {
	counter, err := otel.Meter("github.com/felinics/memoh/internal/agent/application").Int64Counter("context_epoch.prefix_drift_total")
	if err != nil {
		return
	}
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("violation", "stable_prefix_drift")))
}
