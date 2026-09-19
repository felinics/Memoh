package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
)

// followUpPayload is the document stored for one follow-up item. Text is the
// user-visible input and is always present so queue listings can render the
// item. Command is present when the follow-up was a complete turn that arrived
// while the session was busy; it preserves channel routing, attachments, and
// reply metadata so the continuation answers where the message came from.
type followUpPayload struct {
	Text    string                 `json:"text"`
	Command *turn.StartTurnCommand `json:"command,omitempty"`
}

func encodeFollowUpCommand(cmd turn.StartTurnCommand) ([]byte, error) {
	text := strings.TrimSpace(cmd.UserVisibleText)
	if text == "" {
		text = strings.TrimSpace(cmd.Query)
	}
	return json.Marshal(followUpPayload{Text: text, Command: &cmd})
}

func decodeFollowUpPayload(payload []byte) followUpPayload {
	var body followUpPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return followUpPayload{}
	}
	body.Text = strings.TrimSpace(body.Text)
	return body
}

// QueuePayloadText renders only user-visible text. Invalid/empty payloads never
// fall back to the raw envelope, which can contain a deferred command credential.
func QueuePayloadText(payload []byte) string {
	body := decodeFollowUpPayload(payload)
	if text := strings.TrimSpace(body.Text); text != "" {
		return text
	}
	if body.Command != nil {
		if text := strings.TrimSpace(body.Command.UserVisibleText); text != "" {
			return text
		}
		return strings.TrimSpace(body.Command.Query)
	}
	return ""
}

// EnqueueDeferredTurn places a complete user turn that met a busy session into
// the session's follow-up queue. The command is stored intact, so the
// continuation keeps the channel route, attachments, and reply metadata of the
// original message. The caller receives the same admission errors as any other
// follow-up: in particular ErrNoActiveRun means the run ended between the busy
// admission result and this call, and the caller should retry admission.
func (s *Service) EnqueueDeferredTurn(ctx context.Context, cmd turn.StartTurnCommand) error {
	if s == nil || s.sessionManager == nil {
		return errors.New("turn: deferred queue is not configured")
	}
	if strings.TrimSpace(cmd.BotID) == "" || strings.TrimSpace(cmd.ThreadID) == "" {
		return errors.New("turn: deferred turn requires bot and thread")
	}
	payload, err := encodeFollowUpCommand(cmd)
	if err != nil {
		return err
	}
	invocationID := strings.TrimSpace(cmd.IdempotencyKey)
	if invocationID == "" {
		invocationID = uuid.NewString()
	}
	_, err = s.EnqueueFollowUp(ctx, cmd.BotID, cmd.ThreadID, "deferred:"+invocationID, payload)
	return err
}

// kickFollowUpIfIdle closes the admission race for follow-ups: the enqueue
// observed an active run, but that run may have reached its terminal observer
// before the item was written, in which case nobody would claim it until the
// next run of the session ends.
func (s *Service) kickFollowUpIfIdle(ctx context.Context, botID, sessionID, enqueuedDuringRunID string) {
	if s == nil || s.sessionManager == nil || strings.TrimSpace(enqueuedDuringRunID) == "" {
		return
	}
	snapshot, err := s.sessionManager.Snapshot(ctx, botID, sessionID)
	if err != nil {
		return
	}
	// Any active run, including a newer one, will claim the item at its own
	// terminal boundary; only an idle session needs the kick.
	if run := snapshot.CurrentRunView; run != nil && sessionruntime.IsActiveRunStatus(run.Status) {
		return
	}
	s.startFollowUpAfterTerminal(ctx, sessionruntime.TerminalRun{
		RunID: enqueuedDuringRunID, BotID: botID, SessionID: sessionID,
	})
}

// closeSteerQueueForRun rejects the terminal run's unapplied steers. A steer
// targets exactly one run; once that run is terminal it can never enter a
// model step, and leaving it accepted would show a dead item in the queue.
func (s *Service) closeSteerQueueForRun(ctx context.Context, terminal sessionruntime.TerminalRun) {
	if s == nil || s.sessionManager == nil || terminal.RunID == "" || terminal.BotID == "" || terminal.SessionID == "" {
		return
	}
	key := sessionruntime.Key{BotID: terminal.BotID, SessionID: terminal.SessionID}
	if err := s.sessionManager.CloseSteerRun(ctx, key, terminal.RunID); err != nil && !errors.Is(err, sessionruntime.ErrLiveQueueUnavailable) && s.logger != nil {
		s.logger.WarnContext(ctx, "close steer queue for terminal run failed",
			slog.String("run_id", terminal.RunID), slog.Any("error", err))
	}
}

// startFollowUpAfterTerminal hands one transient follow-up to the ordinary
// turn admission path after a run has reached a terminal boundary. The queue
// claim is intentionally separate from run admission: admission remains the
// single owner/fencing authority, while the live queue only selects payload.
//
// Follow-ups are session-bound and start after every terminal state. An
// aborted or failed run does not invalidate input the user queued behind it;
// steers, which are run-bound, are rejected instead by closeSteerQueueForRun.
func (s *Service) startFollowUpAfterTerminal(ctx context.Context, terminal sessionruntime.TerminalRun) {
	if s == nil || s.sessionManager == nil || terminal.RunID == "" || terminal.BotID == "" || terminal.SessionID == "" {
		return
	}
	go s.startFollowUp(ctx, terminal)
}

// followUpStart coalesces terminal/enqueue notifications while admission is in
// flight. Its lifetime ends before output is drained; output delivery must not
// hold the next run's admission gate.
type followUpStart struct {
	mu      sync.Mutex
	closed  bool
	pending *sessionruntime.TerminalRun
}

func (s *Service) startFollowUp(parent context.Context, terminal sessionruntime.TerminalRun) {
	ctx := context.WithoutCancel(parent)
	key := sessionruntime.Key{BotID: terminal.BotID, SessionID: terminal.SessionID}
	state := &followUpStart{}
	for {
		current, busy := s.followUpStarts.LoadOrStore(key.String(), state)
		if !busy {
			break
		}
		active := current.(*followUpStart)
		active.mu.Lock()
		if active.closed {
			active.mu.Unlock()
			continue
		}
		active.pending = &terminal
		active.mu.Unlock()
		return
	}
	for {
		if handle := s.admitFollowUp(ctx, key, terminal); handle != nil {
			// Server-owned handles need a consumer, independently of the short
			// admission loop. Terminal observers can schedule the next item.
			go drainDeferredTurn(handle)
		}
		state.mu.Lock()
		if state.pending != nil {
			terminal = *state.pending
			state.pending = nil
			state.mu.Unlock()
			continue
		}
		state.closed = true
		s.followUpStarts.CompareAndDelete(key.String(), state)
		state.mu.Unlock()
		return
	}
}

func (s *Service) admitFollowUp(ctx context.Context, key sessionruntime.Key, terminal sessionruntime.TerminalRun) turn.RunHandle {
	item, claim, ok, err := s.sessionManager.ClaimNextFollowUp(ctx, key, terminal.RunID)
	if err != nil || !ok {
		return nil
	}
	cmd, ok := s.followUpCommand(item)
	if !ok {
		_ = s.sessionManager.ReleaseFollowUp(ctx, key, claim)
		return nil
	}
	var handle turn.RunHandle
	for attempt := 0; ; attempt++ {
		handle, err = s.StartTurn(ctx, cmd)
		if !errors.Is(err, turn.ErrSessionBusy) || attempt >= 7 {
			break
		}
		// ctx is detached from its parent, so only the backoff bounds the wait.
		time.Sleep(time.Duration(1<<attempt) * 10 * time.Millisecond)
	}
	if err != nil && (!errors.Is(err, turn.ErrDuplicateTurn) || errors.Is(err, sessionruntime.ErrInvocationConflict)) {
		// The item stays accepted; the next terminal boundary claims it again.
		_ = s.sessionManager.ReleaseFollowUp(ctx, key, claim)
		if !errors.Is(err, turn.ErrSessionBusy) && s.logger != nil {
			s.logger.WarnContext(ctx, "start follow-up turn failed",
				slog.String("item_id", string(item.ID)), slog.Any("error", err))
		}
		return nil
	}
	if err := s.sessionManager.ApplyFollowUp(ctx, key, claim); err != nil && s.logger != nil {
		s.logger.WarnContext(ctx, "apply transient follow-up failed",
			slog.String("item_id", string(item.ID)),
			slog.String("trigger_run_id", terminal.RunID),
			slog.Any("error", err),
		)
	}
	return handle
}

// followUpCommand rebuilds the StartTurnCommand for one follow-up item. A
// deferred channel turn carries its full command; a queue-panel follow-up only
// carries text and starts as an ordinary chat turn on the same session.
func (s *Service) followUpCommand(item sessionruntime.FollowUpItem) (turn.StartTurnCommand, bool) {
	body := decodeFollowUpPayload(item.Payload)
	var cmd turn.StartTurnCommand
	if body.Command != nil {
		cmd = *body.Command
		if strings.TrimSpace(cmd.BotID) != item.BotID || strings.TrimSpace(cmd.ThreadID) != item.SessionID {
			return turn.StartTurnCommand{}, false
		}
	} else {
		text := strings.TrimSpace(body.Text)
		if text == "" {
			return turn.StartTurnCommand{}, false
		}
		cmd = turn.StartTurnCommand{
			Mode:            turn.ModeChat,
			BotID:           item.BotID,
			ChatID:          item.BotID,
			ThreadID:        item.SessionID,
			Query:           text,
			UserVisibleText: text,
		}
	}
	// A continuation is server-owned: it never re-enters the deferred queue,
	// and its retry identity is the queue item rather than the original
	// platform message, whose admission attempt already failed as busy.
	cmd.NoDefer = true
	cmd.IdempotencyKey = "follow-up:" + string(item.ID)
	if strings.TrimSpace(cmd.TeamID) == "" {
		// A text-only follow-up has no team of its own. The in-process service
		// serves exactly one team; without it the continuation fails closed, as
		// every other turn admission does for an empty TeamID.
		cmd.TeamID = s.allowedTeam
	}
	if strings.TrimSpace(cmd.TeamID) == "" {
		return turn.StartTurnCommand{}, false
	}
	return cmd, true
}
