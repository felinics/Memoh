package discuss

import (
	"context"
	"log/slog"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/timeline"
)

const discussIdleTimeout = 10 * time.Minute

func (d *DiscussDriver) runSession(ctx context.Context, sess *discussSession) {
	initialConfig := d.sessionConfigSnapshot(sess)
	sessionID := initialConfig.ThreadID
	log := d.logger.With(slog.String("session_id", sessionID), slog.String("bot_id", initialConfig.BotID))
	log.Info("discuss session started")
	defer func() {
		log.Info("discuss session stopped")
		d.mu.Lock()
		if cur, ok := d.sessions[sessionID]; ok && cur == sess {
			delete(d.sessions, sessionID)
		}
		d.mu.Unlock()
	}()

	idle := time.NewTimer(discussIdleTimeout)
	defer idle.Stop()

	var latestRC timeline.RenderedContext
	for {
		select {
		case <-sess.stopCh:
			return
		case <-idle.C:
			log.Info("discuss session idle timeout, exiting")
			return
		case rc := <-sess.rcCh:
			latestRC = rc
			idle.Reset(discussIdleTimeout)
		}

	drain:
		for {
			select {
			case rc := <-sess.rcCh:
				latestRC = rc
			default:
				break drain
			}
		}

		if len(latestRC) == 0 {
			continue
		}
		if !timeline.HasUncoveredExternalEvent(latestRC, sess.lastProcessed) {
			continue
		}
		d.handleReply(ctx, sess, latestRC, log)
	}
}

func (d *DiscussDriver) handleReply(ctx context.Context, sess *discussSession, rc timeline.RenderedContext, log *slog.Logger) {
	d.handleReplyWithTurn(ctx, sess, rc, log, d.turnServiceSnapshot())
}

// loadArtifacts projects the session's active compaction frontier. A load
// failure is surfaced to the caller so composition degrades into the bounded
// admission window instead of silently re-materializing the full raw history
// (CM-ADM-003).
func (d *DiscussDriver) loadArtifacts(ctx context.Context, cfg DiscussSessionConfig) ([]timeline.CompactionArtifact, error) {
	if d.artifacts == nil {
		return nil, nil
	}
	return d.artifacts.ActiveCompactionArtifacts(ctx, cfg.BotID, cfg.ThreadID)
}

// handleReplyWithTurn remains as a narrow seam for parity tests. Production
// workers obtain the current service through turnServiceSnapshot.
func (d *DiscussDriver) handleReplyWithTurn(ctx context.Context, sess *discussSession, rc timeline.RenderedContext, log *slog.Logger, turnSvc turn.Service) {
	cfg := d.sessionConfigSnapshot(sess)
	trs, historyMeasure := d.history.Load(ctx, cfg.ThreadID)
	if historyMeasure.TotalMessages > int64(historyMeasure.Loaded) {
		log.Info("context_admission",
			slog.String("path", "discuss_history_load"),
			slog.Int64("history_total_messages", historyMeasure.TotalMessages),
			slog.Int64("history_total_bytes", historyMeasure.TotalBytes),
			slog.Int("history_loaded_messages", historyMeasure.Loaded),
			slog.Int("budget_tokens", d.admissionMaxTokens()))
	}

	// Cold-start / post-idle initialisation combines the durable position with
	// the persisted-reply anchor; each segment is then gated inside its own
	// cursor or source-time domain.
	if sess.lastProcessed == (timeline.DiscussCursorPosition{}) {
		persisted := d.cursor.Load(ctx, cfg, log)
		sess.lastProcessed = persisted.Merge(timeline.DiscussCursorPosition{SourceCursor: anchorFromTRs(trs)})
	}
	if !timeline.HasUncoveredExternalEvent(rc, sess.lastProcessed) {
		return
	}

	artifacts, artifactsErr := d.loadArtifacts(ctx, cfg)
	if artifactsErr != nil {
		log.Warn("context_admission_degraded",
			slog.String("reason", "artifact_load_failed"),
			slog.Any("error", artifactsErr))
	}
	plan, admission, ok := d.trigger.Build(cfg, rc, trs, sess.lastProcessed, artifacts, timeline.ComposeBudget{MaxTokens: d.admissionMaxTokens()})
	if !ok {
		if admission.ProtectedOverflow {
			// Fail closed without advancing the cursor: nothing was
			// materialized, and a later compaction can shrink the protected
			// set enough for the next attempt to pass.
			log.Error("context_admission_rejected",
				slog.String("code", "context.protected_overflow"),
				slog.Int("estimated_tokens", admission.EstimatedTokens),
				slog.Int("budget_tokens", d.admissionMaxTokens()))
		}
		return
	}
	if admission.DroppedEntries > 0 {
		log.Info("context_admission",
			slog.String("path", "discuss_compose"),
			slog.Int("estimated_tokens", admission.EstimatedTokens),
			slog.Int("selected_tokens", admission.SelectedTokens),
			slog.Int("budget_tokens", d.admissionMaxTokens()),
			slog.Int("dropped_entries", admission.DroppedEntries),
			slog.Int("total_entries", admission.TotalEntries),
			slog.Bool("degraded_artifacts", artifactsErr != nil))
	}
	log.Info("triggering discuss LLM call",
		slog.Int("messages", plan.messageCount),
		slog.Int("estimated_tokens", plan.estimatedTokens))

	if turnSvc == nil {
		log.Error("discuss driver: turn service not configured")
		return
	}
	outcome, started := d.runner.Run(ctx, turnSvc, plan.command, log)
	if !started || outcome.cancelled || outcome.runtimeType == "" {
		return
	}
	if outcome.runtimeType == sessionRuntimeACPAgent {
		if outcome.skipped || (outcome.streamed && outcome.terminal && !outcome.failed) {
			d.cursor.Advance(ctx, sess, cfg, plan.consumed, log)
		}
		return
	}
	d.cursor.Advance(ctx, sess, cfg, plan.consumed, log)
}
