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
	"github.com/felinics/memoh/internal/telemetry"
)

// queuePayload is the document stored for one queue item, steer or
// follow-up. Text is the user-visible input and is always present so queue
// listings can render the item without decoding the command. Command is the
// complete turn the item replays when it is consumed: for a deferred channel
// message it preserves routing, attachments, and reply metadata; for a text
// typed into the queue panel it carries the team and sender the ingress
// authenticated. Items written by earlier releases may lack Command; they are
// still listable but cannot be started, and the continuation rejects them.
type queuePayload struct {
	Text    string                 `json:"text"`
	Command *turn.StartTurnCommand `json:"command,omitempty"`
}

func encodeQueueCommand(cmd turn.StartTurnCommand) ([]byte, error) {
	text := strings.TrimSpace(cmd.UserVisibleText)
	if text == "" {
		text = strings.TrimSpace(cmd.Query)
	}
	return json.Marshal(queuePayload{Text: text, Command: &cmd})
}

func decodeQueuePayload(payload []byte) queuePayload {
	var body queuePayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return queuePayload{}
	}
	body.Text = strings.TrimSpace(body.Text)
	return body
}

// ErrQueueItemNotEditable reports an edit by anyone other than the sender the
// item recorded. A queued command runs as that sender, with their attribution,
// credentials, and memory, so rewriting its text is theirs alone; others with
// queue access can still cancel or reorder it.
var ErrQueueItemNotEditable = errors.New("queue: only the sender can edit a queued item")

// rewriteQueuePayloadText replaces the user text of a stored item while
// keeping the command's sender, routing, and attachment metadata intact. Only
// the recorded sender may edit; an item without a recorded sender user (a
// platform sender with no linked account, or a payload without a command) is
// not editable.
func rewriteQueuePayloadText(payload []byte, editorUserID, text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, sessionruntime.ErrQueueInvalidReference
	}
	body := decodeQueuePayload(payload)
	editor := strings.TrimSpace(editorUserID)
	if body.Command == nil || editor == "" || strings.TrimSpace(body.Command.UserID) != editor {
		return nil, ErrQueueItemNotEditable
	}
	body.Text = text
	body.Command.Query = text
	body.Command.ModelQuery = ""
	body.Command.UserVisibleText = text
	return json.Marshal(body)
}

// QueuePayloadText renders only user-visible text. Invalid/empty payloads never
// fall back to the raw envelope, which can contain a deferred command credential.
func QueuePayloadText(payload []byte) string {
	body := decodeQueuePayload(payload)
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
	if strings.TrimSpace(cmd.TeamID) == "" || strings.TrimSpace(cmd.BotID) == "" || strings.TrimSpace(cmd.ThreadID) == "" {
		return errors.New("turn: deferred turn requires team, bot, and thread")
	}
	invocationID := strings.TrimSpace(cmd.IdempotencyKey)
	if invocationID == "" {
		invocationID = uuid.NewString()
	}
	_, err := s.enqueueFollowUpCommand(ctx, "deferred:"+invocationID, cmd)
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
	// The turn that finished is what let this one start, and it is already
	// over: a follow-up is its own trace, linked to the run it was queued
	// behind rather than nested inside it. Without this the second, third and
	// tenth queued message all land in the first one's trace.
	ctx := telemetry.ContextWithTrigger(context.WithoutCancel(parent), telemetry.TriggerFrom(parent))
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

// admitFollowUp claims the next follow-up for this terminal boundary and
// starts it. An item that cannot be replayed is rejected and the next one is
// tried, so one unreadable item never blocks the rest of the queue. Every
// iteration either returns or moves one item to a terminal status; when the
// rejection itself fails the item stays claimed and the loop must stop, since
// the same claim would come straight back from the backend.
func (s *Service) admitFollowUp(ctx context.Context, key sessionruntime.Key, terminal sessionruntime.TerminalRun) turn.RunHandle {
	for {
		item, claim, ok, err := s.sessionManager.ClaimNextFollowUp(ctx, key, terminal.RunID)
		if err != nil || !ok {
			return nil
		}
		cmd, err := followUpCommand(item)
		if err != nil {
			if s.rejectFollowUp(ctx, key, item, claim, err) != nil {
				return nil
			}
			continue
		}
		return s.startFollowUpCommand(ctx, key, item, claim, cmd)
	}
}

// rejectFollowUp terminalizes an item this deployment can never start and
// reports whether the queue recorded the rejection.
func (s *Service) rejectFollowUp(ctx context.Context, key sessionruntime.Key, item sessionruntime.FollowUpItem, claim sessionruntime.FollowUpClaimRef, cause error) error {
	if s.logger != nil {
		s.logger.WarnContext(ctx, "follow-up item cannot be started; rejecting it",
			slog.String("item_id", string(item.ID)), slog.Any("error", cause))
	}
	err := s.sessionManager.RejectFollowUp(ctx, key, claim, sessionruntime.QueueErrorFollowUpCommandInvalid)
	if err != nil && s.logger != nil {
		s.logger.WarnContext(ctx, "reject follow-up item failed",
			slog.String("item_id", string(item.ID)), slog.Any("error", err))
	}
	return err
}

func (s *Service) startFollowUpCommand(ctx context.Context, key sessionruntime.Key, item sessionruntime.FollowUpItem, claim sessionruntime.FollowUpClaimRef, cmd turn.StartTurnCommand) turn.RunHandle {
	var handle turn.RunHandle
	var err error
	for attempt := 0; ; attempt++ {
		handle, err = s.StartTurn(ctx, cmd)
		if !errors.Is(err, turn.ErrSessionBusy) || attempt >= 7 {
			break
		}
		// ctx is detached from its parent, so only the backoff bounds the wait.
		time.Sleep(time.Duration(1<<attempt) * 10 * time.Millisecond)
	}
	if errors.Is(err, turn.ErrTeamNotServed) {
		// The recorded team is not one this instance serves. That does not
		// change between boundaries, so releasing the item would only retry
		// it forever; it is terminal like an item without a team.
		_ = s.rejectFollowUp(ctx, key, item, claim, err)
		return nil
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
			slog.String("trigger_run_id", claim.TriggerRunID),
			slog.Any("error", err),
		)
	}
	return handle
}

var (
	errFollowUpPayloadWithoutCommand = errors.New("follow-up payload carries no command")
	errFollowUpCommandForeignSession = errors.New("follow-up command belongs to another session")
	errFollowUpCommandWithoutTeam    = errors.New("follow-up command has no team")
	errFollowUpCommandWithoutInput   = errors.New("follow-up command has no query or attachments")
)

// followUpCommand restores the StartTurnCommand stored with one follow-up
// item. The command is the complete admission input the ingress recorded when
// it accepted the text, so the continuation replays it rather than inferring
// team or sender from process state. An item missing any of that cannot be
// started; the error names what is missing so the caller can reject it.
func followUpCommand(item sessionruntime.FollowUpItem) (turn.StartTurnCommand, error) {
	body := decodeQueuePayload(item.Payload)
	if body.Command == nil {
		return turn.StartTurnCommand{}, errFollowUpPayloadWithoutCommand
	}
	cmd := *body.Command
	if strings.TrimSpace(cmd.BotID) != item.BotID || strings.TrimSpace(cmd.ThreadID) != item.SessionID {
		return turn.StartTurnCommand{}, errFollowUpCommandForeignSession
	}
	if strings.TrimSpace(cmd.TeamID) == "" {
		return turn.StartTurnCommand{}, errFollowUpCommandWithoutTeam
	}
	if strings.TrimSpace(cmd.Query) == "" && len(cmd.Attachments) == 0 {
		return turn.StartTurnCommand{}, errFollowUpCommandWithoutInput
	}
	// A continuation is server-owned: it never re-enters the deferred queue,
	// and its retry identity is the queue item rather than the original
	// platform message, whose admission attempt already failed as busy.
	cmd.NoDefer = true
	cmd.IdempotencyKey = "follow-up:" + string(item.ID)
	return cmd, nil
}
