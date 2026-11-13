package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Channel continuations forward the raw agent events the run owner emits after
// an accepted answer. Web reads the session's projected snapshot instead, so a
// channel reader cannot share that state: it needs the raw stream (cards,
// attachments, reactions) and it must survive the owner parking again or a
// notification being lost.
//
// The owner appends each event to a per-command log (DecisionOutputStore); the
// reader keeps a cursor and re-reads from it on every wakeup or tick. Pub/Sub is
// only a wakeup. Run-terminal reconciliation reuses the session snapshot from
// #865/#1107 to notice an owner that died without closing the log.
func decisionOutputRef(botID, commandID string) DecisionOutputRef {
	return DecisionOutputRef{BotID: strings.TrimSpace(botID), CommandID: strings.TrimSpace(commandID)}
}

// Bound retained payloads per command. Overflow is an explicit interrupted
// continuation; it must never silently discard output.
var decisionOutputLimits = DecisionOutputLimits{MaxBytes: 8 << 20, MaxEvents: 32768}

var errDecisionOutputLimitExceeded = errors.New("decision output checkpoint limit exceeded")

func wrapDecisionOutputInterrupted(err error) error {
	if err == nil || errors.Is(err, ErrDecisionOutputInterrupted) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrDecisionOutputInterrupted, err)
}

func mergeDecisionOutputRun(s, r, g *string, src DecisionOutputRun) {
	for _, p := range [][2]*string{{s, &src.SessionID}, {r, &src.RunID}, {g, &src.Generation}} {
		if *p[0] == "" {
			*p[0] = strings.TrimSpace(*p[1])
		}
	}
}

func (s DecisionOutputState) corruptSplitTTL() bool {
	return !s.Released && (s.Done || s.Failed) && s.Bytes > 0 && s.Length == 0
}

func (m *Manager) decisionOutputClaimLeaseTTL() time.Duration {
	if m != nil && m.decisionOutputClaimTTL > 0 {
		return m.decisionOutputClaimTTL
	}
	return defaultDecisionOutputClaimTTL
}

func (m *Manager) finishDecisionOutputClaim(ctx context.Context, ref DecisionOutputRef, token string, release bool) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.commandTimeout())
	defer cancel()
	var err error
	if release {
		err = m.backend.ReleaseDecisionOutput(cleanupCtx, ref, token)
	} else {
		err = m.backend.UnclaimDecisionOutput(cleanupCtx, ref, token)
	}
	if err != nil {
		m.logger.WarnContext(ctx, "finish decision output claim failed", slog.Any("error", err))
	}
}

func (m *Manager) HasPendingDecisionOutput(ctx context.Context, commandType, botID, decisionID, controlID string) bool {
	trimmed := strings.TrimSpace(controlID)
	if m == nil || m.backend == nil || trimmed == "" {
		return false
	}
	cmdID := decisionControlCommandID(commandType, strings.TrimSpace(botID), strings.TrimSpace(decisionID), trimmed)
	for _, id := range []string{cmdID, trimmed} {
		if page, err := m.backend.ReadDecisionOutput(ctx, decisionOutputRef(botID, id), 0); err == nil && page.Exists && !page.Released {
			return true
		}
		if id == trimmed {
			break
		}
	}
	return false
}

func (m *Manager) releaseCompletedDecisionOutput(ctx context.Context, response DecisionResponse) (DecisionResponseResult, error) {
	response.ControlID, response.Type = strings.TrimSpace(response.ControlID), strings.TrimSpace(response.Type)
	response.DecisionID, response.BotID, response.SessionID = strings.TrimSpace(response.DecisionID), strings.TrimSpace(response.BotID), strings.TrimSpace(response.SessionID)
	if response.ControlID == "" || response.DecisionID == "" || response.BotID == "" {
		return DecisionResponseResult{}, errors.New("decision control_id, decision_id, and bot_id are required")
	}
	if response.Type != CommandToolApprovalResponse && response.Type != CommandUserInputResponse {
		return DecisionResponseResult{}, fmt.Errorf("unsupported runtime decision command %q", response.Type)
	}
	ref := decisionOutputRef(response.BotID, decisionControlCommandID(response.Type, response.BotID, response.DecisionID, response.ControlID))
	page, err := m.backend.ReadDecisionOutput(ctx, ref, 0)
	if err != nil {
		return DecisionResponseResult{Handled: true, SessionID: response.SessionID}, wrapDecisionOutputInterrupted(err)
	}
	if !page.Exists {
		m.mu.Lock()
		decisions := m.decisionStore
		m.mu.Unlock()
		if decisions != nil {
			if t, rErr := decisions.ResolveRuntimeDecision(ctx, response.Type, response.DecisionID); rErr == nil && strings.TrimSpace(t.ControlID) != "" {
				origRef := decisionOutputRef(response.BotID, decisionControlCommandID(response.Type, response.BotID, response.DecisionID, t.ControlID))
				if origPage, oErr := m.backend.ReadDecisionOutput(ctx, origRef, 0); oErr == nil && origPage.Exists {
					ref, page = origRef, origPage
				}
			}
		}
	}
	result := DecisionResponseResult{SessionID: response.SessionID, Handled: true, Applied: true, Replayed: true}
	mergeDecisionOutputRun(&result.SessionID, &result.RunID, &result.Generation, page.Run)
	if page.Exists && !page.Released && (page.Done || page.Failed) {
		tok := "release-" + uuid.NewString()
		if outcome, claimErr := m.backend.ClaimDecisionOutput(ctx, ref, tok, m.decisionOutputClaimLeaseTTL()); claimErr != nil {
			m.finishDecisionOutputClaim(ctx, ref, tok, false)
			return result, wrapDecisionOutputInterrupted(claimErr)
		} else if outcome == DecisionOutputClaimAcquired {
			m.finishDecisionOutputClaim(ctx, ref, tok, true)
		}
	}
	return result, nil
}

func (m *Manager) StreamDecisionResponse(ctx context.Context, response DecisionResponse, output chan<- json.RawMessage) (DecisionResponseResult, error) {
	if m != nil && m.backend != nil && response.ReleaseOutput {
		return m.releaseCompletedDecisionOutput(ctx, response)
	}
	if m == nil || m.backend == nil || output == nil {
		return m.RouteDecisionResponse(ctx, response)
	}
	response.BotID = strings.TrimSpace(response.BotID)
	ref := decisionOutputRef(response.BotID, decisionControlCommandID(response.Type, response.BotID, response.DecisionID, response.ControlID))
	sub, err := m.backend.Subscribe(ctx, ref.topic())
	if err != nil {
		return DecisionResponseResult{}, err
	}
	defer func() {
		if sub.Close != nil {
			sub.Close()
		}
	}()

	claimToken, leaseTTL := uuid.NewString(), m.decisionOutputClaimLeaseTTL()
	response.streamOutput, response.streamClaimToken, response.streamClaimTTL = true, claimToken, leaseTTL
	result, err := m.RouteDecisionResponse(ctx, response)
	if err != nil {
		m.finishDecisionOutputClaim(ctx, ref, claimToken, false)
		if page, readErr := m.backend.ReadDecisionOutput(context.WithoutCancel(ctx), ref, 0); readErr == nil && page.Exists {
			return result, wrapDecisionOutputInterrupted(err)
		}
		return result, err
	}
	if !result.Handled {
		return result, nil
	}
	if !result.Applied {
		if result.recoveredCommandID == "" {
			return result, nil
		}
		origRef := decisionOutputRef(response.BotID, result.recoveredCommandID)
		origPage, pageErr := m.backend.ReadDecisionOutput(ctx, origRef, 0)
		if pageErr != nil {
			return result, wrapDecisionOutputInterrupted(pageErr)
		}
		if !origPage.Exists || origPage.Released {
			return result, nil
		}
		if sub.Close != nil {
			sub.Close()
			sub.Close = nil
		}
		if sub, err = m.backend.Subscribe(ctx, origRef.topic()); err != nil {
			result.Applied = true
			return result, wrapDecisionOutputInterrupted(err)
		}
		ref, result.Applied, result.Replayed = origRef, true, true
		mergeDecisionOutputRun(&result.SessionID, &result.RunID, &result.Generation, origPage.Run)
	}

	page, pageErr := m.backend.ReadDecisionOutput(ctx, ref, 0)
	if pageErr != nil {
		m.finishDecisionOutputClaim(ctx, ref, claimToken, false)
		return result, wrapDecisionOutputInterrupted(pageErr)
	}
	if !page.Exists || page.Released {
		result.Replayed = true
		return result, nil
	}
	mergeDecisionOutputRun(&result.SessionID, &result.RunID, &result.Generation, page.Run)
	if page.corruptSplitTTL() {
		m.finishDecisionOutputClaim(ctx, ref, "", true)
		return result, io.ErrUnexpectedEOF
	}

	switch outcome, err := m.backend.ClaimDecisionOutput(ctx, ref, claimToken, leaseTTL); {
	case err != nil:
		m.finishDecisionOutputClaim(ctx, ref, claimToken, false)
		return result, wrapDecisionOutputInterrupted(err)
	case outcome == DecisionOutputClaimBusy:
		if !response.DeferRelease && result.coalesced && result.recoveredCommandID == "" && !result.Replayed {
			result.Replayed = true
			return result, nil
		}
		return result, fmt.Errorf("%w: %w", ErrDecisionOutputInterrupted, ErrDecisionOutputBusy)
	case outcome == DecisionOutputClaimUnavailable:
		if recheck, rErr := m.backend.ReadDecisionOutput(ctx, ref, 0); rErr == nil && recheck.corruptSplitTTL() {
			m.finishDecisionOutputClaim(ctx, ref, "", true)
			return result, io.ErrUnexpectedEOF
		}
		result.Replayed = true
		return result, nil
	}
	result.Replayed = false

	accepted, _ := json.Marshal(map[string]string{"type": "decision_accepted", "decision_id": response.DecisionID})
	select {
	case output <- accepted:
	case <-ctx.Done():
		m.finishDecisionOutputClaim(ctx, ref, claimToken, false)
		return result, wrapDecisionOutputInterrupted(ctx.Err())
	}
	response.SessionID = result.SessionID
	readErr := m.readClaimedDecisionOutput(ctx, response, result.RunID, result.Generation, ref, sub, output, claimToken, leaseTTL)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		m.finishDecisionOutputClaim(ctx, ref, claimToken, false)
		return result, wrapDecisionOutputInterrupted(readErr)
	}
	return result, readErr
}

func (m *Manager) readDecisionOutput(ctx context.Context, response DecisionResponse, runID, generation string, ref DecisionOutputRef, sub Subscription, output chan<- json.RawMessage) error {
	return m.readClaimedDecisionOutput(ctx, response, runID, generation, ref, sub, output, "", 0)
}

func (m *Manager) readClaimedDecisionOutput(ctx context.Context, response DecisionResponse, runID, generation string, ref DecisionOutputRef, sub Subscription, output chan<- json.RawMessage, claimToken string, leaseTTL time.Duration) error {
	cursor := 0
	terminalObserved := false
	ticker := time.NewTicker(runtimeReconcileInterval(m.ownerLeaseTTL))
	defer ticker.Stop()
	var renewCh <-chan time.Time
	if claimToken != "" {
		ttl := leaseTTL
		if ttl <= 0 {
			ttl = defaultDecisionOutputClaimTTL
		}
		renewTicker := time.NewTicker(max(ttl/3, 10*time.Millisecond))
		defer renewTicker.Stop()
		renewCh = renewTicker.C
	}
	renewLease := func() error {
		if owned, err := m.backend.RenewDecisionOutputClaim(ctx, ref, claimToken, leaseTTL); claimToken == "" || err != nil || owned {
			return err
		}
		return fmt.Errorf("%w: claim lease lost", ErrDecisionOutputInterrupted)
	}
	// drain forwards everything committed past the cursor. It is the only source
	// of truth; wakeups and ticks just decide when to call it.
	drain := func() (bool, error) {
		page, err := m.backend.ReadDecisionOutput(ctx, ref, cursor)
		if err != nil {
			return false, err
		}
		if !page.Released && page.Bytes > 0 && page.Length == 0 {
			return false, io.ErrUnexpectedEOF
		}
		for _, raw := range page.Events {
			for sent := false; !sent; {
				select {
				case output <- raw:
					cursor++
					sent = true
				case <-renewCh:
					if err := renewLease(); err != nil {
						return false, err
					}
				case <-ctx.Done():
					return false, ctx.Err()
				}
			}
		}
		if page.Failed {
			return false, io.ErrUnexpectedEOF
		}
		return page.Done, nil
	}
	// finish decides whether the reader stops. Every error stops it, including a
	// backend read failure: waiting on would hide an interrupted delivery until
	// the request context expires.
	finish := func(done bool, err error) (bool, error) {
		if err == nil && !done {
			return false, nil
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || (done && !response.DeferRelease) {
			m.finishDecisionOutputClaim(ctx, ref, claimToken, true)
		} else if done && claimToken != "" {
			m.finishDecisionOutputClaim(ctx, ref, claimToken, false)
		}
		return true, err
	}
	for {
		if stop, err := finish(drain()); stop {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-renewCh:
			if err := renewLease(); err != nil {
				return err
			}
		case _, ok := <-sub.C:
			if !ok {
				return fmt.Errorf("%w: subscription closed", ErrDecisionOutputInterrupted)
			}
			// Any event, including a dropped-notification marker, is a wakeup.
		case <-ticker.C:
			if runID == "" {
				continue
			}
			// An owner crash does not close another process's Redis subscription.
			// Reuse the run snapshot's existing ledger/lease reconciliation instead.
			live, err := m.Snapshot(ctx, response.BotID, response.SessionID)
			if err != nil {
				return err
			}
			ended := live.CurrentRunView == nil || live.CurrentRunView.RunID != runID || !isActiveRunStatus(live.CurrentRunView.Status)
			if !ended && generation != "" && live.CurrentRunView.Generation != generation {
				ended = true
			}
			if !ended {
				terminalObserved = false
				continue
			}
			if stop, err := finish(drain()); stop {
				return err
			}
			if terminalObserved {
				m.finishDecisionOutputClaim(ctx, ref, claimToken, true)
				return io.ErrUnexpectedEOF
			}
			// FinishRun can precede the continuation's deferred close. Give that
			// writer one reconciliation interval, rather than guessing that a
			// terminal run proves its output was delivered.
			terminalObserved = true
		}
	}
}

// PublishDecisionOutput appends one raw event (or closes the log with a nil
// payload) and then wakes readers. Commit before notify, exactly as runtime UI
// deltas do; a lost wakeup costs latency, never data.
func (m *Manager) PublishDecisionOutput(ctx context.Context, command Command, seq int64, payload json.RawMessage) error {
	if !command.StreamOutput {
		return nil
	}
	ref := decisionOutputRef(command.BotID, command.ID)
	state, err := m.backend.AppendDecisionOutput(ctx, ref, seq, payload, decisionOutputLimits)
	if err != nil {
		return err
	}
	if state.Applied {
		topic := ref.topic()
		if err := m.backend.Publish(ctx, Event{
			Type: EventDecisionOutput, BotID: topic.BotID, SessionID: topic.SessionID,
			RunID: command.RunID, Seq: int64(state.Length),
		}); err != nil {
			m.logger.WarnContext(ctx, "publish decision output wakeup failed; reader will reconcile from log", slog.Any("error", err))
		}
	}
	if state.Exceeded {
		return errDecisionOutputLimitExceeded
	}
	return nil
}
