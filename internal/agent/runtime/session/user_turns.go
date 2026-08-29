package sessionruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	chatview "github.com/felinics/memoh/internal/agent/view"
)

type QueueUserTurnUpdate struct {
	PersistedTurns        []chatview.UITurn
	AppliedSteerItemID    string
	AppliedSteerTurn      *chatview.UITurn
	ClaimedSteerItemID    string
	ClaimedSteerText      string
	ClaimedSteerTimestamp time.Time
}

// PublishPersistedUserTurns keeps the non-queue projection entry point used by
// ordinary runtime history publication.
func (m *Manager) PublishPersistedUserTurns(ctx context.Context, handle RunHandle, turns []chatview.UITurn) error {
	return m.PublishQueueUserTurns(ctx, handle, QueueUserTurnUpdate{PersistedTurns: turns})
}

// PublishQueueUserTurns projects one committed queue step into the live run.
// The update is atomic so applying one steer and claiming the next cannot
// briefly render them out of order. A claimed steer is shown only after the
// coordinator committed its claim and execution accepted the resulting input.
func (m *Manager) PublishQueueUserTurns(ctx context.Context, handle RunHandle, update QueueUserTurnUpdate) error {
	if m == nil || m.backend == nil {
		return nil
	}
	normalized := make([]chatview.UITurn, 0, len(update.PersistedTurns))
	for i := range update.PersistedTurns {
		turn, err := normalizeRequestUserTurn(&update.PersistedTurns[i])
		if err != nil {
			return err
		}
		if turn == nil || strings.TrimSpace(turn.TurnID) == "" {
			return errors.New("persisted runtime user turn requires turn_id")
		}
		normalized = append(normalized, *turn)
	}
	var appliedTurn *chatview.UITurn
	if update.AppliedSteerTurn != nil {
		turn, err := normalizeRequestUserTurn(update.AppliedSteerTurn)
		if err != nil {
			return err
		}
		if turn == nil || strings.TrimSpace(turn.TurnID) == "" {
			return errors.New("applied steer runtime turn requires turn_id")
		}
		appliedTurn = turn
	}

	published := make([]chatview.UITurn, 0, len(normalized))
	steerUpserts := make([]SteerTurnView, 0, 2)
	_, _, err := m.updateActiveAndPublish(ctx, handle, func(snapshot Snapshot, now time.Time) (Snapshot, bool, error) {
		run := snapshot.CurrentRunView
		if !runMatchesHandle(run, handle) || !m.runOwnerMatches(run) || !isActiveRunStatus(run.Status) {
			return snapshot, false, ErrRunOwnershipLost
		}
		if len(run.UserTurns) == 0 {
			switch {
			case run.RequestUserTurn != nil:
				run.UserTurns = append(run.UserTurns, *run.RequestUserTurn)
			case run.Operation != nil && run.Operation.ReplacementUserTurn != nil:
				run.UserTurns = append(run.UserTurns, *run.Operation.ReplacementUserTurn)
			}
		}
		changed := false
		for _, incoming := range normalized {
			index := -1
			for i := range run.UserTurns {
				if strings.TrimSpace(run.UserTurns[i].TurnID) == strings.TrimSpace(incoming.TurnID) {
					index = i
					break
				}
			}
			if index >= 0 {
				if reflect.DeepEqual(run.UserTurns[index], incoming) {
					continue
				}
				run.UserTurns[index] = incoming
			} else {
				run.UserTurns = append(run.UserTurns, incoming)
			}
			published = append(published, incoming)
			changed = true
		}
		appliedItemID := strings.TrimSpace(update.AppliedSteerItemID)
		if appliedItemID != "" {
			index := steerTurnIndex(run.SteerTurns, appliedItemID)
			if index < 0 {
				run.SteerTurns = append(run.SteerTurns, SteerTurnView{
					ItemID: appliedItemID, Status: "applied", Text: steerTurnText(appliedTurn),
					TurnID: steerTurnID(appliedTurn), AfterMessageID: maxRuntimeMessageID(run.Messages), Timestamp: now,
				})
				index = len(run.SteerTurns) - 1
			} else {
				run.SteerTurns[index].Status = "applied"
				if appliedTurn != nil {
					run.SteerTurns[index].TurnID = strings.TrimSpace(appliedTurn.TurnID)
					run.SteerTurns[index].Timestamp = appliedTurn.Timestamp
				}
			}
			steerUpserts = append(steerUpserts, run.SteerTurns[index])
			changed = true
		}
		claimedItemID := strings.TrimSpace(update.ClaimedSteerItemID)
		if claimedItemID != "" {
			claimedAt := update.ClaimedSteerTimestamp
			if claimedAt.IsZero() {
				claimedAt = now
			}
			incoming := SteerTurnView{
				ItemID: claimedItemID, Status: "claimed", Text: strings.TrimSpace(update.ClaimedSteerText),
				AfterMessageID: maxRuntimeMessageID(run.Messages), Timestamp: claimedAt,
			}
			index := steerTurnIndex(run.SteerTurns, claimedItemID)
			if index < 0 {
				run.SteerTurns = append(run.SteerTurns, incoming)
			} else {
				incoming.AfterMessageID = run.SteerTurns[index].AfterMessageID
				run.SteerTurns[index] = incoming
			}
			steerUpserts = append(steerUpserts, incoming)
			changed = true
		}
		if !changed {
			return snapshot, false, nil
		}
		snapshot.Seq++
		snapshot.UpdatedAt = now
		run.UpdatedAt = now
		return snapshot, true, nil
	}, func(Snapshot) RuntimeDelta {
		return RuntimeDelta{
			UserTurnUpserts:  append([]chatview.UITurn(nil), published...),
			SteerTurnUpserts: append([]SteerTurnView(nil), steerUpserts...),
		}
	})
	return err
}

func steerTurnIndex(turns []SteerTurnView, itemID string) int {
	for i := range turns {
		if strings.TrimSpace(turns[i].ItemID) == itemID {
			return i
		}
	}
	return -1
}

func maxRuntimeMessageID(messages []chatview.UIMessage) int {
	maximum := -1
	for i := range messages {
		if messages[i].ID > maximum {
			maximum = messages[i].ID
		}
	}
	return maximum
}

func steerTurnID(turn *chatview.UITurn) string {
	if turn == nil {
		return ""
	}
	return strings.TrimSpace(turn.TurnID)
}

func steerTurnText(turn *chatview.UITurn) string {
	if turn == nil {
		return ""
	}
	return strings.TrimSpace(turn.Text)
}
