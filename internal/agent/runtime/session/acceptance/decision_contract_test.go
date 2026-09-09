//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/felinics/memoh/internal/agent/turn"
	turntransport "github.com/felinics/memoh/internal/agent/turn/grpctransport"
	intrpc "github.com/felinics/memoh/internal/rpc"
)

// TestSRDEC001LiveDecisionContinuesSameRun is the always-on decision baseline.
// It deliberately avoids process or backend faults so both the single-memory
// and two-Server Valkey topologies must run it on every acceptance pass.
func TestSRDEC001LiveDecisionContinuesSameRun(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "decision-live")
	marker := uniqueMarker("decision-live")
	invocationID := "invocation-" + marker
	text := directiveMode(marker, 2, 10, "ask_user") + " continue this run"
	env := loadEnvironment()

	owner := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(owner)
	observer := mustDial(t, peerURL(env), fixture)
	defer closeWebSocket(observer)
	mustSubscribeAndReadSnapshot(t, owner, sessionID)
	mustSubscribeAndReadSnapshot(t, observer, sessionID)

	_, admitted := mustSendAndAccept(t, fixture, owner, sessionID, invocationID, text)
	waitingEvents, err := readUntil(observer, 8*time.Second, func(event wsEvent) bool {
		return eventRunID(event) == admitted.RunID && eventState(event) == "waiting_decision"
	})
	if err != nil {
		t.Fatalf("SR-DEC-001: observer did not see waiting_decision: %v; events=%#v", err, waitingEvents)
	}
	waiting := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "waiting_decision"
	})
	decision := mustPendingUserInput(t, waiting)

	answers, err := firstDecisionAnswer(decision.UIPayload)
	if err != nil {
		t.Fatalf("build answer from decision payload: %v", err)
	}
	controlID := "control-" + uniqueMarker("decision-live")
	if err := sendUserInputResponse(observer, sessionID, waiting.RunID, decision.ID, controlID, answers); err != nil {
		t.Fatalf("answer live decision through peer Server: %v", err)
	}
	ack := mustReadControlAck(t, observer, controlID)
	if !ack.Applied || ack.Control != "user_input_response" || eventRunID(ack) != waiting.RunID || eventCode(ack) != "" {
		t.Fatalf("SR-DEC-001: live decision ack = %#v, want applied response for run %s", ack, waiting.RunID)
	}
	mustReadRunCompleted(t, observer, waiting.RunID)

	completed := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	assertDecisionRunCompletedOnce(t, completed, decision.ID, marker, "submitted")
}

// TestSRCTL001DecisionControlIDReplaysAfterTerminal proves that control_id,
// rather than the currently active projection, is the request identity. The
// replay intentionally happens after the run has disappeared from live state
// and its Redis result was removed, so PostgreSQL must answer from the decision
// row itself.
func TestSRCTL001DecisionControlIDReplaysAfterTerminal(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "decision-control-replay")
	marker := uniqueMarker("decision-control")
	invocationID := "invocation-" + marker
	text := directiveMode(marker, 1, 0, "ask_user") + " replay this control"
	env := loadEnvironment()

	owner := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(owner)
	responder := mustDial(t, peerURL(env), fixture)
	mustSubscribeAndReadSnapshot(t, owner, sessionID)
	mustSubscribeAndReadSnapshot(t, responder, sessionID)

	_, admitted := mustSendAndAccept(t, fixture, owner, sessionID, invocationID, text)
	waiting := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "waiting_decision"
	})
	decision := mustPendingUserInput(t, waiting)
	answers, err := firstDecisionAnswer(decision.UIPayload)
	if err != nil {
		t.Fatalf("build answer from decision payload: %v", err)
	}
	controlID := "control-" + uniqueMarker("decision-replay")
	if err := sendUserInputResponse(responder, sessionID, admitted.RunID, decision.ID, controlID, answers); err != nil {
		t.Fatalf("send initial decision control: %v", err)
	}
	firstAck := mustReadControlAck(t, responder, controlID)
	if !firstAck.Applied {
		t.Fatalf("initial decision control was not applied: %#v", firstAck)
	}
	mustReadRunCompleted(t, responder, waiting.RunID)
	completed := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	assertDecisionRunCompletedOnce(t, completed, decision.ID, marker, "submitted")
	controlCtx, cancelControl := context.WithTimeout(context.Background(), databaseTimeout)
	storedControl, err := requireLedger(t).userInputResponseIdentity(controlCtx, decision.ID)
	cancelControl()
	if err != nil {
		t.Fatalf("read persisted decision response identity: %v", err)
	}
	if storedControl.ControlID != controlID || storedControl.PayloadHash == "" {
		t.Fatalf("persisted decision response identity = %#v", storedControl)
	}
	closeWebSocket(responder)

	if env.mode != "single" {
		// Remove the fast Redis replay cache. The retry below must still return
		// the original applied result from PostgreSQL.
		if err := deleteAcceptanceDecisionResult(
			context.Background(),
			env.redisURL,
			"user_input_response",
			fixture.botID,
			decision.ID,
			controlID,
		); err != nil {
			t.Fatalf("remove live decision result cache: %v", err)
		}
	}
	replayer := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(replayer)
	if err := sendUserInputResponse(replayer, sessionID, admitted.RunID, decision.ID, controlID, answers); err != nil {
		t.Fatalf("replay terminal decision control: %v", err)
	}
	replayAck := mustReadControlAck(t, replayer, controlID)
	assertSameControlAck(t, firstAck, replayAck)

	freshControlID := "control-" + uniqueMarker("decision-stale")
	if err := sendUserInputResponse(replayer, sessionID, admitted.RunID, decision.ID, freshControlID, answers); err != nil {
		t.Fatalf("send stale decision under fresh control id: %v", err)
	}
	staleAck := mustReadControlAck(t, replayer, freshControlID)
	if staleAck.Applied || eventCode(staleAck) != "" {
		t.Errorf(
			"stale decision ack = %#v, want a resolved no-op with applied=false and no retry code",
			staleAck,
		)
	}
}

// TestSRDEC001DecisionTransportParity holds every decision transport to the
// same durable run contract. The cluster topology sends both responses to the
// peer Server: public HTTP for tool approval, and the authenticated Turn gRPC
// endpoint used by a standalone Channel process for user input.
func TestSRDEC001DecisionTransportParity(t *testing.T) {
	t.Run("http tool approval resumes the active run", testHTTPToolApprovalParity)
	t.Run("turn grpc user input resumes the active run", testTurnGRPCUserInputParity)
}

func testHTTPToolApprovalParity(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)
	if err := fixture.api.setToolApprovalRequired(fixture.botID, true); err != nil {
		t.Fatalf("enable approval-required exec policy: %v", err)
	}
	defer func() {
		if err := fixture.api.setToolApprovalRequired(fixture.botID, false); err != nil {
			t.Errorf("restore approval policy: %v", err)
		}
	}()

	sessionID := mustCreateSession(t, fixture, "decision-http-approval")
	marker := uniqueMarker("decision-http")
	invocationID := "invocation-" + marker
	text := directiveMode(marker, 1, 0, "tool_approval") + " approve through HTTP"
	env := loadEnvironment()

	owner := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(owner)
	observer := mustDial(t, peerURL(env), fixture)
	defer closeWebSocket(observer)
	mustSubscribeAndReadSnapshot(t, owner, sessionID)
	mustSubscribeAndReadSnapshot(t, observer, sessionID)

	_, admitted := mustSendAndAccept(t, fixture, owner, sessionID, invocationID, text)
	waitingEvents, err := readUntil(observer, 8*time.Second, func(event wsEvent) bool {
		return eventRunID(event) == admitted.RunID && eventState(event) == "waiting_decision"
	})
	if err != nil {
		t.Fatalf("HTTP parity run did not reach waiting_decision: %v; events=%#v", err, waitingEvents)
	}
	waiting := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "waiting_decision"
	})
	approval := mustPendingToolApproval(t, waiting)

	peerAPI := fixture.api.forBaseURL(peerURL(env))
	if err := peerAPI.approveTool(fixture.botID, approval.ID); err != nil {
		t.Fatalf("approve active run through peer HTTP API: %v", err)
	}
	mustReadRunCompleted(t, observer, waiting.RunID)
	completed := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	assertToolApprovalRunCompletedOnce(t, completed, approval.ID, marker)
}

func testTurnGRPCUserInputParity(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "decision-turn-grpc")
	marker := uniqueMarker("decision-grpc")
	invocationID := "invocation-" + marker
	text := directiveMode(marker, 2, 10, "ask_user") + " answer through Turn RPC"
	env := loadEnvironment()

	owner := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(owner)
	observer := mustDial(t, peerURL(env), fixture)
	defer closeWebSocket(observer)
	mustSubscribeAndReadSnapshot(t, owner, sessionID)
	mustSubscribeAndReadSnapshot(t, observer, sessionID)

	_, admitted := mustSendAndAccept(t, fixture, owner, sessionID, invocationID, text)

	waitingEvents, err := readUntil(observer, 8*time.Second, func(event wsEvent) bool {
		return eventRunID(event) == admitted.RunID && eventState(event) == "waiting_decision"
	})
	if err != nil {
		t.Fatalf("run for Turn RPC response did not publish waiting_decision: %v; events=%#v", err, waitingEvents)
	}
	waiting := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "waiting_decision"
	})
	decision := mustPendingUserInput(t, waiting)
	answers, err := firstTurnDecisionAnswer(decision.UIPayload)
	if err != nil {
		t.Fatalf("build Turn RPC decision answer: %v", err)
	}

	peerRPC := env.primaryRPC
	if env.mode != "single" {
		peerRPC = env.secondaryRPC
	}
	peerConn, err := intrpc.Dial(peerRPC, env.rpcSecret)
	if err != nil {
		t.Fatalf("dial peer Turn RPC: %v", err)
	}
	defer func() { _ = peerConn.Close() }()
	peer := turntransport.NewClient(peerConn)
	responseCtx, cancelResponse := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelResponse()
	continuation, err := respondUserInputOverTurnRPC(responseCtx, peer, turn.UserInputResponse{
		BotID:                      fixture.botID,
		ThreadID:                   sessionID,
		UserInputID:                decision.ID,
		ExplicitID:                 decision.ID,
		Answers:                    answers,
		SuppressActivePromptAttach: true,
	})
	if err != nil {
		t.Fatalf("respond through peer Turn RPC: %v; events=%s", err, formatRawEvents(continuation))
	}

	mustReadRunCompleted(t, observer, waiting.RunID)
	completed := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	assertDecisionRunCompletedOnce(t, completed, decision.ID, marker, "submitted")
}

func respondUserInputOverTurnRPC(ctx context.Context, client *turntransport.Client, input turn.UserInputResponse) ([]json.RawMessage, error) {
	events := make(chan json.RawMessage, 64)
	done := make(chan error, 1)
	go func() {
		done <- client.RespondUserInput(ctx, input, events)
		close(events)
	}()
	var collected []json.RawMessage
	for event := range events {
		collected = append(collected, append(json.RawMessage(nil), event...))
	}
	return collected, <-done
}

func firstTurnDecisionAnswer(payload json.RawMessage) ([]turn.QuestionAnswer, error) {
	answers, err := firstDecisionAnswer(payload)
	if err != nil {
		return nil, err
	}
	result := make([]turn.QuestionAnswer, 0, len(answers))
	for _, answer := range answers {
		questionID := stringValue(answer["question_id"])
		var rawOptionIDs []string
		switch optionIDs := answer["option_ids"].(type) {
		case []string:
			rawOptionIDs = append(rawOptionIDs, optionIDs...)
		case []any:
			for _, raw := range optionIDs {
				if optionID, ok := raw.(string); ok {
					rawOptionIDs = append(rawOptionIDs, optionID)
				}
			}
		}
		result = append(result, turn.QuestionAnswer{QuestionID: questionID, OptionIDs: rawOptionIDs})
	}
	return result, nil
}

func mustPendingUserInput(t *testing.T, run sessionRunRecord) userInputRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	decision, err := requireLedger(t).waitPendingUserInput(ctx, run.RunID)
	if err != nil {
		t.Fatalf("wait for pending user input linked to run: %v", err)
	}
	if decision.RunID != run.RunID || decision.TurnID != run.TurnID {
		t.Fatalf("user input linkage = run %q turn %q, want %q %q", decision.RunID, decision.TurnID, run.RunID, run.TurnID)
	}
	if decision.RuntimeFencingToken <= 0 || decision.RuntimeFencingToken != run.FencingToken {
		t.Fatalf(
			"user input fencing token = %d, run = %d",
			decision.RuntimeFencingToken,
			run.FencingToken,
		)
	}
	return decision
}

func mustPendingToolApproval(t *testing.T, run sessionRunRecord) toolApprovalRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	approval, err := requireLedger(t).waitPendingToolApproval(ctx, run.RunID)
	if err != nil {
		t.Fatalf("wait for pending tool approval linked to run: %v", err)
	}
	if approval.RunID != run.RunID || approval.TurnID != run.TurnID {
		t.Fatalf("tool approval linkage = run %q turn %q, want %q %q", approval.RunID, approval.TurnID, run.RunID, run.TurnID)
	}
	if approval.RuntimeFencingToken <= 0 || approval.RuntimeFencingToken != run.FencingToken {
		t.Fatalf(
			"tool approval fencing token = %d, run = %d",
			approval.RuntimeFencingToken,
			run.FencingToken,
		)
	}
	return approval
}

func mustReadControlAck(t *testing.T, connection *websocket.Conn, controlID string) wsEvent {
	t.Helper()
	events, err := readUntil(connection, 8*time.Second, func(event wsEvent) bool {
		return event.Type == "control_ack" && event.ControlID == controlID
	})
	if err != nil {
		t.Fatalf("wait for control_ack %s: %v; events=%#v", controlID, err, events)
	}
	return events[len(events)-1]
}

func mustReadRunCompleted(t *testing.T, connection *websocket.Conn, runID string) {
	t.Helper()
	events, err := readUntil(connection, eventTimeout, func(event wsEvent) bool {
		if eventRunID(event) != runID {
			return false
		}
		switch eventState(event) {
		case "completed", "aborted", "failed", "lost", "errored":
			return event.Type == "runtime_delta" || event.Type == "runtime_snapshot"
		default:
			return false
		}
	})
	if err != nil {
		t.Fatalf("wait for completed runtime event for %s: %v; events=%#v", runID, err, events)
	}
	terminal := events[len(events)-1]
	if state := eventState(terminal); state != "completed" {
		t.Fatalf("run %s terminated as %q, want completed; event=%#v", runID, state, terminal)
	}
}

func assertSameControlAck(t *testing.T, first, replay wsEvent) {
	t.Helper()
	if replay.Applied != first.Applied ||
		replay.Type != first.Type ||
		replay.SessionID != first.SessionID ||
		replay.Control != first.Control ||
		replay.ControlID != first.ControlID ||
		eventRunID(replay) != eventRunID(first) ||
		eventCode(replay) != eventCode(first) {
		t.Errorf("control replay changed: first=%#v replay=%#v", first, replay)
	}
}

func assertDecisionRunCompletedOnce(t *testing.T, run sessionRunRecord, decisionID, marker, wantStatus string) {
	t.Helper()
	assertOneRunForSession(t, run.SessionID)
	assertTerminalHistory(t, run)
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	status, err := requireLedger(t).userInputStatus(ctx, decisionID)
	if err != nil || status != wantStatus {
		t.Errorf("user input status = %q, err=%v, want %q", status, err, wantStatus)
	}
	if count := globalFakeModel.RequestCount(marker); count != 2 {
		t.Errorf("model calls = %d, want initial decision + resumed completion", count)
	}
}

func assertToolApprovalRunCompletedOnce(t *testing.T, run sessionRunRecord, approvalID, marker string) {
	t.Helper()
	assertOneRunForSession(t, run.SessionID)
	assertTerminalHistory(t, run)
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	status, err := requireLedger(t).toolApprovalStatus(ctx, approvalID)
	if err != nil || status != "approved" {
		t.Errorf("tool approval status = %q, err=%v, want approved", status, err)
	}
	if count := globalFakeModel.RequestCount(marker); count != 2 {
		t.Errorf("model calls = %d, want initial approval + resumed completion", count)
	}
}

func assertOneRunForSession(t *testing.T, sessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	count, err := requireLedger(t).sessionRunCount(ctx, sessionID)
	if err != nil || count != 1 {
		t.Errorf("session_runs rows = %d, err=%v, want exactly 1", count, err)
	}
}

func formatRawEvents(events []json.RawMessage) string {
	encoded, err := json.Marshal(events)
	if err != nil {
		return fmt.Sprint(events)
	}
	return string(encoded)
}
