package application

import (
	"context"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/felinics/memoh/internal/agent/runtime/native"
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
	if state.BudgetTokens > 0 {
		cfg.PinnedHistoryBudgetTokens = int(state.BudgetTokens)
	}
	pinnedHash := state.PrefixHash
	epoch := state.Epoch
	cfg.RecordHistoryBudgetPin = func(pinCtx context.Context, tokens int) {
		if _, err := s.contextEpochs.PinBudget(pinCtx, sessionID, int64(tokens)); err != nil {
			s.logger.WarnContext(pinCtx, "context_epoch_budget_pin_failed",
				slog.String("session_id", sessionID),
				slog.Int("budget_tokens", tokens),
				slog.Any("error", err))
		}
	}
	cfg.RecordStablePrefixHash = func(reportCtx context.Context, observed string) {
		s.reportStablePrefixHash(reportCtx, sessionID, epoch, pinnedHash, observed)
	}
}

// reportStablePrefixHash applies the rule-1 accounting for one rendered turn:
// the epoch's first observation pins the hash; when the pin already exists
// (from this turn's load or a racing turn's pin), any other value is drift.
func (s *Service) reportStablePrefixHash(ctx context.Context, sessionID string, epoch int64, pinnedHash, observed string) {
	if observed == "" {
		return
	}
	pinned := pinnedHash
	if pinned == "" {
		stored, err := s.contextEpochs.PinPrefixHash(ctx, sessionID, observed)
		if err != nil {
			s.logger.WarnContext(ctx, "context_epoch_prefix_hash_pin_failed",
				slog.String("session_id", sessionID),
				slog.Any("error", err))
			return
		}
		pinned = stored.PrefixHash
		epoch = stored.Epoch
	}
	if pinned == "" || pinned == observed {
		return
	}
	recordPrefixDrift(ctx)
	s.logger.ErrorContext(ctx, "context_epoch_prefix_drift",
		slog.String("session_id", sessionID),
		slog.Int64("epoch", epoch),
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
