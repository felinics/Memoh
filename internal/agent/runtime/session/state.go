package sessionruntime

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	chatview "github.com/felinics/memoh/internal/agent/view"
)

func runtimeDeltaForAgentEvent(event native.StreamEvent, messages []chatview.UIMessage) (RuntimeDelta, bool) {
	switch event.Type {
	case native.EventAgentStart:
		return RuntimeDelta{}, true
	case native.EventAgentEnd, native.EventAgentAbort, native.EventError:
		return RuntimeDelta{MessageUpserts: append([]chatview.UIMessage(nil), messages...)}, true
	case native.EventRetry:
		return RuntimeDelta{ResetMessages: true}, true
	case native.EventTextDelta, native.EventReasoningDelta:
		if event.Delta == "" || len(messages) == 0 {
			return RuntimeDelta{}, false
		}
		message := messages[len(messages)-1]
		return RuntimeDelta{MessageAppends: []RuntimeMessageAppend{{
			ID:      message.ID,
			Type:    message.Type,
			Content: event.Delta,
		}}}, true
	case native.EventToolCallProgress:
		if len(messages) == 0 {
			return RuntimeDelta{}, false
		}
		message := messages[len(messages)-1]
		return RuntimeDelta{ProgressAppends: []RuntimeProgressAppend{{
			ID:       message.ID,
			Progress: event.Progress,
			Input:    event.Input,
		}}}, true
	default:
		if len(messages) == 0 {
			return RuntimeDelta{}, false
		}
		return RuntimeDelta{MessageUpserts: append([]chatview.UIMessage(nil), messages...)}, true
	}
}

// pendingDecisionEvent distinguishes the initial request from a later
// authoritative status update for the same decision. Both use the same stream
// event vocabulary so the UI converter can update one tool block in place, but
// only the initial pending event parks the run in waiting_decision.
func pendingDecisionEvent(event native.StreamEvent) bool {
	switch event.Type {
	case native.EventToolApprovalRequest, native.EventUserInputRequest:
	default:
		return false
	}
	status := strings.TrimSpace(event.Status)
	return status == "" || strings.EqualFold(status, "pending")
}

func runtimeRunPatch(snapshot Snapshot, status, runError, steer, lease bool) RuntimeDelta {
	run := snapshot.CurrentRunView
	if run == nil {
		return RuntimeDelta{}
	}
	updatedAt := run.UpdatedAt
	patch := &CurrentRunPatch{
		RunID:     run.RunID,
		UpdatedAt: &updatedAt,
	}
	if status {
		value := run.Status
		patch.Status = &value
	}
	if runError {
		code := run.ErrorCode
		patch.ErrorCode = &code
		value := run.Error
		patch.Error = &value
	}
	if steer && run.Steer != nil {
		value := *run.Steer
		patch.Steer = &value
	}
	if lease {
		value := time.Time{}
		if run.OwnerLeaseExpiresAt != nil {
			value = *run.OwnerLeaseExpiresAt
		}
		patch.OwnerLeaseExpiresAt = &value
	}
	return RuntimeDelta{Run: patch}
}

// leaseExpired is independent of process-local control. Once the backend
// lease expires, the old owner must not revive it even if its goroutine resumes.
func (*Manager) leaseExpired(run *CurrentRunView, now time.Time) bool {
	if run == nil || !isActiveRunStatus(run.Status) || run.OwnerLeaseExpiresAt == nil || now.Before(*run.OwnerLeaseExpiresAt) {
		return false
	}
	return true
}

func isActiveRunStatus(status string) bool {
	return strings.EqualFold(status, RunStatusAdmitting) ||
		strings.EqualFold(status, RunStatusRunning) ||
		strings.EqualFold(status, RunStatusWaitingDecision) ||
		strings.EqualFold(status, RunStatusAborting)
}

func isEventAcceptingRunStatus(status string) bool {
	return strings.EqualFold(status, RunStatusRunning) ||
		strings.EqualFold(status, RunStatusWaitingDecision) ||
		strings.EqualFold(status, RunStatusAborting)
}

func (m *Manager) markLostIfExpired(snapshot *Snapshot, now time.Time) bool {
	if snapshot == nil || !m.leaseExpired(snapshot.CurrentRunView, now) {
		return false
	}
	run := snapshot.CurrentRunView
	snapshot.Seq++
	snapshot.UpdatedAt = now
	run.Status = RunStatusLost
	run.Error = "runtime owner lease expired"
	run.UpdatedAt = now
	run.OwnerLeaseExpiresAt = nil
	return true
}

func streamLeaseExpiry(snapshot Snapshot, runID string, fallback time.Time) time.Time {
	if snapshot.CurrentRunView != nil && snapshot.CurrentRunView.RunID == runID && snapshot.CurrentRunView.OwnerLeaseExpiresAt != nil {
		return *snapshot.CurrentRunView.OwnerLeaseExpiresAt
	}
	return fallback
}

func runtimeEventEpoch(event Event) string {
	if epoch := strings.TrimSpace(event.Epoch); epoch != "" {
		return epoch
	}
	if event.Snapshot != nil {
		return strings.TrimSpace(event.Snapshot.Epoch)
	}
	return ""
}

func runRefForRun(botID, sessionID string, run *CurrentRunView) RunRef {
	if run == nil {
		return RunRef{}
	}
	return RunRef{
		BotID:      strings.TrimSpace(botID),
		SessionID:  strings.TrimSpace(sessionID),
		RunID:      strings.TrimSpace(run.RunID),
		OwnerID:    strings.TrimSpace(run.OwnerID),
		Generation: strings.TrimSpace(run.Generation),
	}
}

func runMatchesHandle(run *CurrentRunView, handle RunHandle) bool {
	handle = handle.normalized()
	return run != nil && run.RunID == handle.RunID && run.Generation == handle.Generation
}

func (m *Manager) runRefForControl(ctrl *runControl) RunRef {
	if ctrl == nil {
		return RunRef{}
	}
	ownerID := ""
	if m != nil && m.distributed != nil {
		ownerID = m.ownerID
	}
	return RunRef{
		BotID:      ctrl.botID,
		SessionID:  ctrl.sessionID,
		RunID:      ctrl.runID,
		OwnerID:    ownerID,
		Generation: ctrl.generation,
	}
}

func normalizeRunOperation(operation *RunOperationView) (*RunOperationView, error) {
	if operation == nil {
		return nil, nil
	}
	kind := strings.ToLower(strings.TrimSpace(operation.Kind))
	replaceFrom := strings.TrimSpace(operation.ReplaceFromMessageID)
	if replaceFrom == "" {
		return nil, errors.New("runtime operation replace_from_message_id is required")
	}
	if kind != RunOperationRetry && kind != RunOperationEdit {
		return nil, fmt.Errorf("unsupported runtime operation %q", operation.Kind)
	}
	clone := &RunOperationView{
		Kind:                 kind,
		ReplaceFromMessageID: replaceFrom,
	}
	if operation.ReplacementUserTurn != nil {
		turn := *operation.ReplacementUserTurn
		turn.Attachments = append([]chatview.UIAttachment(nil), turn.Attachments...)
		clone.ReplacementUserTurn = &turn
	}
	if kind == RunOperationEdit && (clone.ReplacementUserTurn == nil || !strings.EqualFold(strings.TrimSpace(clone.ReplacementUserTurn.Role), "user")) {
		return nil, errors.New("edit runtime operation requires a replacement user turn")
	}
	return clone, nil
}

func normalizeRunAdmission(admission RunAdmissionView) (RunAdmissionView, error) {
	operation, err := normalizeRunOperation(admission.Operation)
	if err != nil {
		return RunAdmissionView{}, err
	}
	requestUserTurn, err := normalizeRequestUserTurn(admission.RequestUserTurn)
	if err != nil {
		return RunAdmissionView{}, err
	}
	return RunAdmissionView{RequestUserTurn: requestUserTurn, Operation: operation}, nil
}

func normalizeRequestUserTurn(turn *chatview.UITurn) (*chatview.UITurn, error) {
	if turn == nil {
		return nil, nil
	}
	if !strings.EqualFold(strings.TrimSpace(turn.Role), "user") {
		return nil, errors.New("runtime request_user_turn must have role user")
	}
	clone := *turn
	clone.Role = "user"
	clone.Attachments = append([]chatview.UIAttachment(nil), turn.Attachments...)
	clone.Messages = append([]chatview.UIMessage(nil), turn.Messages...)
	return &clone, nil
}

func leaseRenewInterval(ttl time.Duration) time.Duration {
	interval := ttl / 3
	if interval <= 0 {
		return 100 * time.Millisecond
	}
	if interval < 10*time.Millisecond {
		return 10 * time.Millisecond
	}
	return interval
}

func runtimeReconcileInterval(ownerLeaseTTL time.Duration) time.Duration {
	interval := ownerLeaseTTL / 3
	if interval < 50*time.Millisecond {
		return 50 * time.Millisecond
	}
	if interval > 2*time.Second {
		return 2 * time.Second
	}
	return interval
}

func enqueueRuntimeEvent(ch chan Event, event Event) {
	select {
	case ch <- event:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- Event{
		Type:      EventRuntimeDropped,
		BotID:     event.BotID,
		SessionID: event.SessionID,
		Epoch:     event.Epoch,
		RunID:     event.RunID,
		Seq:       event.Seq,
		Message:   "runtime subscriber buffer overflow",
	}:
	default:
	}
}

func upsertUIMessage(messages []chatview.UIMessage, incoming chatview.UIMessage) []chatview.UIMessage {
	for i := range messages {
		if incoming.Type == chatview.UIMessageTool && strings.TrimSpace(incoming.ToolCallID) != "" && messages[i].Type == chatview.UIMessageTool && messages[i].ToolCallID == incoming.ToolCallID {
			messages[i] = incoming
			return messages
		}
		if messages[i].ID == incoming.ID {
			messages[i] = incoming
			return messages
		}
	}
	return append(messages, incoming)
}
