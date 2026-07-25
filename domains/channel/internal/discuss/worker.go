package discuss

import (
	"context"
	"log/slog"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/timeline"
)

const discussIdleTimeout = 10 * time.Minute

func (d *DiscussDriver) runSession(ctx context.Context, sess *discussSession) {
	initialConfig := d.sessionConfigSnapshot(sess)
	sessionID := initialConfig.ThreadID
	log := d.logger.With(slog.String("session_id", sessionID), slog.String("bot_id", initialConfig.BotID))
	log.InfoContext(ctx, "discuss session started")
	defer func() {
		log.InfoContext(ctx, "discuss session stopped")
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
			log.InfoContext(ctx, "discuss session idle timeout, exiting")
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
		if timeline.LatestExternalEventMs(latestRC, sess.lastProcessedMs) == 0 {
			continue
		}
		d.handleReply(ctx, sess, latestRC, log)
	}
}

func (d *DiscussDriver) handleReply(ctx context.Context, sess *discussSession, rc timeline.RenderedContext, log *slog.Logger) {
	d.handleReplyWithTurn(ctx, sess, rc, log, d.turnServiceSnapshot())
}

// handleReplyWithTurn remains as a narrow seam for parity tests. Production
// workers obtain the current service through turnServiceSnapshot.
func (d *DiscussDriver) handleReplyWithTurn(ctx context.Context, sess *discussSession, rc timeline.RenderedContext, log *slog.Logger, turnSvc agentdomain.Service) {
	cfg := d.sessionConfigSnapshot(sess)
	trs := d.history.Load(ctx, cfg.ThreadID)

	// Cold-start / post-idle initialisation anchors the in-memory cursor to
	// both persisted replies and the durable discuss cursor.
	if sess.lastProcessedMs == 0 {
		sess.lastProcessedMs = maxInt64(anchorFromTRs(trs), d.cursor.Load(ctx, cfg, log))
	}
	if timeline.LatestExternalEventMs(rc, sess.lastProcessedMs) == 0 {
		return
	}

	plan, ok := d.trigger.Build(cfg, rc, trs, sess.lastProcessedMs)
	if !ok {
		return
	}
	log.InfoContext(ctx, "triggering discuss LLM call",
		slog.Int("messages", plan.messageCount),
		slog.Int("estimated_tokens", plan.estimatedTokens))

	if turnSvc == nil {
		log.ErrorContext(ctx, "discuss driver: turn service not configured")
		return
	}
	outcome, started := d.runner.Run(ctx, turnSvc, plan.command, log)
	if !started || outcome.cancelled || outcome.runtimeType == "" {
		return
	}
	if outcome.runtimeType == sessionRuntimeACPAgent {
		if outcome.skipped || (outcome.streamed && outcome.terminal && !outcome.failed) {
			d.cursor.Advance(ctx, sess, cfg, plan.consumedMs, log)
		}
		return
	}
	d.cursor.Advance(ctx, sess, cfg, plan.consumedMs, log)
}
