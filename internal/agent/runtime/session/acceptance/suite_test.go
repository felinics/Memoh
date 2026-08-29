//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/felinics/memoh/internal/apperror"
)

const (
	eventTimeout    = 20 * time.Second
	databaseTimeout = 10 * time.Second
)

var markerSequence atomic.Uint64

func TestSRBASE001BaselineCompletesAndPersists(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "baseline")
	marker := uniqueMarker("baseline")
	invocationID := "invocation-" + marker
	text := directive(marker, 3, 30) + " baseline"

	connection := mustDial(t, loadEnvironment().primaryURL, fixture)
	defer closeWebSocket(connection)
	mustSubscribeAndReadSnapshot(t, connection, sessionID)
	accepted, admitted := mustSendAndAccept(t, fixture, connection, sessionID, invocationID, text)
	events, terminalEvent := mustReadRunTerminal(t, connection, admitted.RunID)

	if !containsPartialText(events) {
		t.Errorf("SR-BASE-001: runtime stream did not emit text deltas: %#v", events)
	}
	if eventRunID(terminalEvent) != admitted.RunID {
		t.Errorf("SR-BASE-001: terminal event run_id = %q, want %q", eventRunID(terminalEvent), admitted.RunID)
	}
	if eventTurnID(accepted) != "" && eventTurnID(accepted) != admitted.TurnID {
		t.Errorf("SR-BASE-001: accepted turn_id = %q, ledger = %q", eventTurnID(accepted), admitted.TurnID)
	}
	if !globalFakeModel.WaitIdle(10 * time.Second) {
		t.Fatal("fake model did not complete the baseline request")
	}
	if count := globalFakeModel.RequestCount(marker); count != 1 {
		t.Errorf("SR-BASE-001: model executions = %d, want 1", count)
	}

	terminal := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	assertTerminalHistory(t, terminal)
	lastChunk := fmt.Sprintf("%s-chunk-%02d", marker, 2)
	if !eventsContainString(events, lastChunk) {
		t.Errorf("SR-BASE-001: WebSocket result does not contain %q: %#v", lastChunk, events)
	}
	history, err := fixture.api.history(fixture.botID, sessionID)
	if err != nil {
		t.Fatalf("query baseline history through public HTTP API: %v", err)
	}
	if !historyContainsRoleText(history, "user", text) {
		t.Errorf("SR-BASE-001: HTTP history has no admitted user input: %#v", history)
	}
	if !historyContainsRoleText(history, "assistant", lastChunk) {
		t.Errorf("SR-BASE-001: HTTP history does not match WebSocket final output %q: %#v", lastChunk, history)
	}
}

func TestSROBS001ReconnectReceivesAuthoritativeSnapshot(t *testing.T) {
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "reconnect-snapshot")
	marker := uniqueMarker("snapshot")
	invocationID := "invocation-" + marker
	text := directive(marker, 50, 100) + " reconnect snapshot"

	primary := mustDial(t, loadEnvironment().primaryURL, fixture)
	mustSubscribeAndReadSnapshot(t, primary, sessionID)
	accepted, admitted := mustSendAndAccept(t, fixture, primary, sessionID, invocationID, text)
	events, err := readUntil(primary, 10*time.Second, isPartialText)
	if err != nil {
		_ = primary.Close()
		t.Fatalf("wait for partial output: %v; events=%#v", err, events)
	}
	_ = primary.Close()
	t.Cleanup(func() {
		if !globalFakeModel.WaitIdle(10 * time.Second) {
			t.Error("fake model did not become idle after reconnect scenario")
		}
	})

	secondary := mustDial(t, loadEnvironment().secondaryURL, fixture)
	defer closeWebSocket(secondary)
	cursor := map[string]any{"epoch": eventEpoch(accepted), "seq": eventSeq(accepted)}
	if err := subscribeRuntime(secondary, sessionID, cursor); err != nil {
		t.Fatalf("subscribe to runtime after reconnect: %v", err)
	}
	snapshotEvents, err := readUntil(secondary, 5*time.Second, func(event wsEvent) bool {
		return event.Type == "runtime_snapshot" && event.SessionID == sessionID
	})
	if err != nil {
		t.Fatalf("SR-OBS-001: reconnect did not return runtime_snapshot: %v; events=%#v", err, snapshotEvents)
	}
	snapshot := snapshotEvents[len(snapshotEvents)-1]
	assertSnapshotIdentity(t, snapshot, admitted)
	if eventEpoch(snapshot) == "" || eventSeq(snapshot) < eventSeq(accepted) {
		t.Errorf(
			"SR-OBS-001: snapshot cursor = (%q,%d), accepted = (%q,%d)",
			eventEpoch(snapshot),
			eventSeq(snapshot),
			eventEpoch(accepted),
			eventSeq(accepted),
		)
	}
}

func TestSROBS003ConcurrentSubscribersConvergeAfterInitiatorDisconnects(t *testing.T) {
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "concurrent-subscribers")
	marker := uniqueMarker("subscribers")
	invocationID := "invocation-" + marker
	text := directive(marker, 30, 75) + " concurrent subscribers"
	env := loadEnvironment()

	initiator := mustDial(t, env.primaryURL, fixture)
	observer := mustDial(t, env.secondaryURL, fixture)
	defer closeWebSocket(observer)
	mustSubscribeAndReadSnapshot(t, initiator, sessionID)
	mustSubscribeAndReadSnapshot(t, observer, sessionID)

	_, admitted := mustSendAndAccept(t, fixture, initiator, sessionID, invocationID, text)
	observerEvents, err := readUntil(observer, 8*time.Second, func(event wsEvent) bool {
		return event.Type == "runtime_delta" && eventRunID(event) == admitted.RunID
	})
	if err != nil {
		_ = initiator.Close()
		t.Fatalf("SR-OBS-003: observer did not see admitted run: %v; events=%#v", err, observerEvents)
	}
	_ = initiator.Close()

	continued, err := readUntil(observer, eventTimeout, func(event wsEvent) bool {
		return eventRunID(event) == admitted.RunID && isTerminal(event)
	})
	if err != nil {
		t.Fatalf("SR-OBS-003: observer did not reach terminal after initiator disconnected: %v; events=%#v", err, continued)
	}
	terminal := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	if terminal.RunID != admitted.RunID || terminal.TurnID != admitted.TurnID {
		t.Errorf("SR-OBS-003: observer/ledger identity diverged: admitted=%#v terminal=%#v", admitted, terminal)
	}
	assertOrderedRunEvents(t, append(observerEvents, continued...), admitted.RunID)
}

func TestSROBS003EditPublishesAuthoritativeReplacementToEverySubscriber(t *testing.T) {
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "edit-subscribers")
	initialMarker := uniqueMarker("edit-old")
	initialInvocationID := "invocation-" + initialMarker
	initialText := directive(initialMarker, 1, 0) + " old prompt"
	env := loadEnvironment()

	initiator := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(initiator)
	observer := mustDial(t, env.secondaryURL, fixture)
	defer closeWebSocket(observer)
	mustSubscribeAndReadSnapshot(t, initiator, sessionID)
	mustSubscribeAndReadSnapshot(t, observer, sessionID)

	_, initialRun := mustSendAndAccept(t, fixture, initiator, sessionID, initialInvocationID, initialText)
	mustReadRunTerminal(t, initiator, initialRun.RunID)
	if !globalFakeModel.WaitIdle(10 * time.Second) {
		t.Fatal("fake model did not complete the initial edit fixture")
	}
	initialHistory, err := fixture.api.history(fixture.botID, sessionID)
	if err != nil {
		t.Fatalf("load initial edit history: %v", err)
	}
	oldUserMessageID := historyMessageIDByRole(initialHistory, "user")
	if oldUserMessageID == "" {
		t.Fatalf("initial history has no user message id: %#v", initialHistory)
	}
	positionBeforeEdit, err := requireLedger(t).nextTurnPosition(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("read next turn position before edit: %v", err)
	}

	editMarker := uniqueMarker("edit-new")
	editInvocationID := "invocation-" + editMarker
	editedText := directive(editMarker, 3, 20) + " edited prompt"
	if err := sendEdit(initiator, sessionID, editInvocationID, oldUserMessageID, editedText); err != nil {
		t.Fatalf("send edit: %v", err)
	}
	_, accepted, err := readAccepted(initiator, editInvocationID, eventTimeout)
	if err != nil {
		t.Fatalf("read edit acceptance: %v", err)
	}
	observerEvents, err := readUntil(observer, eventTimeout, func(event wsEvent) bool {
		return event.Type == "runtime_delta" &&
			eventRunID(event) == accepted.RunID &&
			nestedString(event.Delta, "replace_from_message_id") == oldUserMessageID
	})
	if err != nil {
		t.Fatalf("SR-OBS-003: observer did not receive the edit boundary: %v; events=%#v", err, observerEvents)
	}
	operationEvent := observerEvents[len(observerEvents)-1]
	if got := nestedString(operationEvent.Delta, "kind"); got != "edit" {
		t.Errorf("SR-OBS-003: operation kind = %q, want edit", got)
	}
	if got := nestedString(operationEvent.Delta, "text"); got != editedText {
		t.Errorf("SR-OBS-003: replacement user text = %q, want %q", got, editedText)
	}
	continued, err := readUntil(observer, eventTimeout, func(event wsEvent) bool {
		return eventRunID(event) == accepted.RunID && isTerminal(event)
	})
	if err != nil {
		t.Fatalf("SR-OBS-003: observer did not reach edited terminal state: %v; events=%#v", err, continued)
	}
	terminal := mustWaitRunState(t, sessionID, editInvocationID, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	if terminal.RunID != accepted.RunID || terminal.TurnID != eventTurnID(accepted) {
		t.Errorf("SR-OBS-003: edit identity diverged: accepted=%#v terminal=%#v", accepted, terminal)
	}
	if terminal.TurnPosition != positionBeforeEdit {
		t.Errorf("SR-TURN-001: edit turn_position = %d, want %d", terminal.TurnPosition, positionBeforeEdit)
	}
	positionAfterEdit, err := requireLedger(t).nextTurnPosition(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("read next turn position after edit: %v", err)
	}
	if positionAfterEdit != positionBeforeEdit+1 {
		t.Errorf("SR-TURN-001: edit advanced next_turn_position from %d to %d, want %d", positionBeforeEdit, positionAfterEdit, positionBeforeEdit+1)
	}
	assertTerminalHistory(t, terminal)
	finalHistory, err := fixture.api.history(fixture.botID, sessionID)
	if err != nil {
		t.Fatalf("load edited history: %v", err)
	}
	if !historyContainsRoleText(finalHistory, "user", editedText) {
		t.Errorf("SR-OBS-003: edited user turn is missing from history: %#v", finalHistory)
	}
	if historyContainsRoleText(finalHistory, "user", initialText) {
		t.Errorf("SR-OBS-003: superseded user turn remains visible: %#v", finalHistory)
	}
}

func TestSRCTL001ReconnectAbortReachesOwnerAndIsAcknowledged(t *testing.T) {
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "reconnect-abort")
	marker := uniqueMarker("abort")
	invocationID := "invocation-" + marker
	text := directiveMode(marker, 1, 0, "block") + " reconnect abort"
	env := loadEnvironment()

	primary := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(primary)
	_, admitted := mustSendAndAccept(t, fixture, primary, sessionID, invocationID, text)
	defer globalFakeModel.Release(marker)
	if !globalFakeModel.WaitRequestCount(marker, 1, 5*time.Second) {
		t.Fatal("blocked abort run did not reach fake model")
	}

	secondary := mustDial(t, env.secondaryURL, fixture)
	defer closeWebSocket(secondary)
	controlID := "control-" + uniqueMarker("abort")
	if err := sendAbort(secondary, sessionID, admitted.RunID, controlID); err != nil {
		t.Fatalf("send abort through secondary Server: %v", err)
	}
	ackEvents, ackErr := readUntil(secondary, 5*time.Second, func(event wsEvent) bool {
		return event.Type == "control_ack" &&
			eventRunID(event) == admitted.RunID &&
			event.ControlID == controlID
	})
	if ackErr != nil {
		t.Fatalf("SR-CTL-001: abort did not receive control_ack: %v; events=%#v", ackErr, ackEvents)
	}
	ack := ackEvents[len(ackEvents)-1]
	if !ack.Applied || ack.Control != "abort" {
		t.Errorf("SR-CTL-001: abort ack = %#v, want applied abort", ack)
	}
	if err := sendAbort(secondary, sessionID, admitted.RunID, controlID); err != nil {
		t.Fatalf("repeat abort through secondary Server: %v", err)
	}
	retryEvents, retryErr := readUntil(secondary, 5*time.Second, func(event wsEvent) bool {
		return event.Type == "control_ack" && event.ControlID == controlID
	})
	if retryErr != nil {
		t.Fatalf("SR-CTL-001: repeated abort did not receive an idempotent ack: %v; events=%#v", retryErr, retryEvents)
	}
	retryAck := retryEvents[len(retryEvents)-1]
	if retryAck.Applied != ack.Applied || retryAck.Control != ack.Control ||
		eventRunID(retryAck) != eventRunID(ack) || eventCode(retryAck) != eventCode(ack) {
		t.Errorf("SR-CTL-001: repeated abort ack changed: first=%#v retry=%#v", ack, retryAck)
	}
	if !globalFakeModel.WaitDisconnected(marker, 5*time.Second) {
		t.Error("SR-CTL-001: owner did not cancel the upstream model request")
	}
	aborted := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "aborted"
	})
	if !aborted.AbortRequested {
		t.Error("SR-CTL-001: aborted run did not persist abort_requested_at")
	}
}

func TestSRADM001DuplicateInvocationConvergesOnOneLedgerRow(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "duplicate-invocation")
	marker := uniqueMarker("duplicate")
	invocationID := "invocation-" + marker
	text := directive(marker, 12, 80) + " duplicate invocation"
	env := loadEnvironment()

	primary := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(primary)
	secondary := mustDial(t, peerURL(env), fixture)
	defer closeWebSocket(secondary)

	start := make(chan struct{})
	sendErrors := make(chan error, 2)
	for _, connection := range []*websocket.Conn{primary, secondary} {
		go func(connection *websocket.Conn) {
			<-start
			sendErrors <- sendChat(connection, sessionID, invocationID, text)
		}(connection)
	}
	close(start)
	for range 2 {
		if err := <-sendErrors; err != nil {
			t.Fatalf("send duplicate invocation: %v", err)
		}
	}

	type acceptedResult struct {
		event wsEvent
		err   error
	}
	acceptedResults := make(chan acceptedResult, 2)
	for _, connection := range []*websocket.Conn{primary, secondary} {
		go func(connection *websocket.Conn) {
			_, event, err := readAccepted(connection, invocationID, eventTimeout)
			acceptedResults <- acceptedResult{event: event, err: err}
		}(connection)
	}
	first := <-acceptedResults
	second := <-acceptedResults
	if first.err != nil || second.err != nil {
		t.Fatalf("SR-ADM-001: duplicate submissions did not both receive run_accepted: first=%v second=%v", first.err, second.err)
	}
	if eventRunID(first.event) == "" || eventRunID(first.event) != eventRunID(second.event) {
		t.Fatalf("SR-ADM-001: duplicate run ids differ: first=%#v second=%#v", first.event, second.event)
	}
	if eventTurnID(first.event) == "" || eventTurnID(first.event) != eventTurnID(second.event) {
		t.Fatalf("SR-ADM-001: duplicate turn ids differ: first=%#v second=%#v", first.event, second.event)
	}
	if !first.event.Duplicate && !second.event.Duplicate {
		t.Error("SR-ADM-001: neither duplicate response was marked duplicate")
	}

	ledger := requireLedger(t)
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	run, err := ledger.waitRun(ctx, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	if err != nil {
		t.Fatalf("wait for duplicate invocation ledger row: %v", err)
	}
	if count, err := ledger.runCount(ctx, sessionID, invocationID); err != nil || count != 1 {
		t.Errorf("SR-ADM-001: ledger rows = %d, err=%v, want 1", count, err)
	}
	if count := globalFakeModel.RequestCount(marker); count != 1 {
		t.Errorf("SR-ADM-001: model executions = %d, want 1", count)
	}
	assertTerminalHistory(t, run)
}

func TestSRADM001FingerprintConflictIsStableAndDoesNotCreateRun(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "invocation-conflict")
	marker := uniqueMarker("conflict")
	invocationID := "invocation-" + marker
	original := directive(marker, 2, 10) + " original input"

	primary := mustDial(t, loadEnvironment().primaryURL, fixture)
	defer closeWebSocket(primary)
	mustSubscribeAndReadSnapshot(t, primary, sessionID)
	_, admitted := mustSendAndAccept(t, fixture, primary, sessionID, invocationID, original)
	mustReadRunTerminal(t, primary, admitted.RunID)

	secondary := mustDial(t, peerURL(loadEnvironment()), fixture)
	defer closeWebSocket(secondary)
	if err := sendChat(secondary, sessionID, invocationID, original+" changed"); err != nil {
		t.Fatalf("send conflicting invocation: %v", err)
	}
	events, rejected, err := readRejected(
		secondary,
		invocationID,
		string(apperror.CodeSessionInvocationConflict),
		5*time.Second,
	)
	if err != nil {
		t.Fatalf("SR-ADM-001: conflicting replay was not rejected: %v; events=%#v", err, events)
	}
	if eventRunID(rejected) != "" && eventRunID(rejected) != admitted.RunID {
		t.Errorf("SR-ADM-001: conflict referenced run %q, want original %q", eventRunID(rejected), admitted.RunID)
	}
	if err := sendChat(secondary, sessionID, invocationID, original+" changed again"); err != nil {
		t.Fatalf("repeat conflicting invocation: %v", err)
	}
	retryEvents, retryRejected, retryErr := readRejected(
		secondary,
		invocationID,
		string(apperror.CodeSessionInvocationConflict),
		5*time.Second,
	)
	if retryErr != nil {
		t.Fatalf("SR-ADM-001: repeated conflict was not stable: %v; events=%#v", retryErr, retryEvents)
	}
	if eventCode(retryRejected) != eventCode(rejected) {
		t.Errorf("SR-ADM-001: conflict code changed: first=%#v retry=%#v", rejected, retryRejected)
	}
	ledger := requireLedger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if count, err := ledger.runCount(ctx, sessionID, invocationID); err != nil || count != 1 {
		t.Errorf("SR-ADM-001: conflict changed ledger row count = %d, err=%v", count, err)
	}
	if count := globalFakeModel.RequestCount(marker); count != 1 {
		t.Errorf("SR-ADM-001: conflict triggered %d model calls, want 1", count)
	}
}

func TestSROWN001BusyWritesNothingAndRetryLaterSucceeds(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "same-session-busy")
	firstMarker := uniqueMarker("owner")
	secondMarker := uniqueMarker("busy")
	firstInvocation := "invocation-" + firstMarker
	secondInvocation := "invocation-" + secondMarker
	env := loadEnvironment()

	primary := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(primary)
	_, firstRun := mustSendAndAccept(
		t,
		fixture,
		primary,
		sessionID,
		firstInvocation,
		directiveMode(firstMarker, 1, 0, "block")+" first owner",
	)
	defer globalFakeModel.Release(firstMarker)
	if !globalFakeModel.WaitRequestCount(firstMarker, 1, 5*time.Second) {
		t.Fatal("first owner did not reach fake model")
	}

	ledger := requireLedger(t)
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	positionBefore, err := ledger.nextTurnPosition(ctx, sessionID)
	if err != nil {
		t.Fatalf("read next_turn_position before busy: %v", err)
	}

	secondary := mustDial(t, peerURL(env), fixture)
	defer closeWebSocket(secondary)
	secondText := directive(secondMarker, 2, 10) + " second invocation"
	if err := sendChat(secondary, sessionID, secondInvocation, secondText); err != nil {
		t.Fatalf("send busy invocation: %v", err)
	}
	events, _, err := readRejected(
		secondary,
		secondInvocation,
		string(apperror.CodeSessionBusy),
		5*time.Second,
	)
	if err != nil {
		t.Fatalf("SR-OWN-001: second invocation was not rejected busy: %v; events=%#v", err, events)
	}
	if count, err := ledger.runCount(ctx, sessionID, secondInvocation); err != nil || count != 0 {
		t.Errorf("SR-OWN-001: busy invocation ledger rows = %d, err=%v, want 0", count, err)
	}
	if active, err := ledger.activeRunCount(ctx, sessionID); err != nil || active != 1 {
		t.Errorf("SR-OWN-001: active ledger rows = %d, err=%v, want 1", active, err)
	}
	if positionAfter, err := ledger.nextTurnPosition(ctx, sessionID); err != nil || positionAfter != positionBefore {
		t.Errorf("SR-OWN-001: busy changed next_turn_position from %d to %d, err=%v", positionBefore, positionAfter, err)
	}
	if count := globalFakeModel.RequestCount(secondMarker); count != 0 {
		t.Errorf("SR-OWN-001: busy invocation reached model %d times", count)
	}

	globalFakeModel.Release(firstMarker)
	mustWaitRunState(t, sessionID, firstInvocation, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	if err := sendChat(secondary, sessionID, secondInvocation, secondText); err != nil {
		t.Fatalf("retry former busy invocation: %v", err)
	}
	_, accepted, err := readAccepted(secondary, secondInvocation, eventTimeout)
	if err != nil {
		t.Fatalf("SR-OWN-001: former busy invocation was not admitted after slot freed: %v", err)
	}
	if eventRunID(accepted) == "" || eventRunID(accepted) == firstRun.RunID {
		t.Errorf("SR-OWN-001: retry run identity = %#v, first=%s", accepted, firstRun.RunID)
	}
	secondRun := mustWaitRunState(t, sessionID, secondInvocation, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	if secondRun.TurnPosition != positionBefore {
		t.Errorf("SR-OWN-001: admitted retry turn_position = %d, want %d", secondRun.TurnPosition, positionBefore)
	}
	if positionAfter, err := ledger.nextTurnPosition(ctx, sessionID); err != nil || positionAfter != positionBefore+1 {
		t.Errorf("SR-OWN-001: admitted retry next_turn_position = %d, err=%v, want %d", positionAfter, err, positionBefore+1)
	}
}

func TestLedgerDoesNotHeartbeatWhileRunIsActive(t *testing.T) {
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "ledger-write-budget")
	marker := uniqueMarker("write-budget")
	invocationID := "invocation-" + marker
	text := directiveMode(marker, 1, 0, "block") + " no PostgreSQL lease heartbeat"

	connection := mustDial(t, loadEnvironment().primaryURL, fixture)
	defer closeWebSocket(connection)
	_, admitted := mustSendAndAccept(t, fixture, connection, sessionID, invocationID, text)
	defer globalFakeModel.Release(marker)
	if !globalFakeModel.WaitRequestCount(marker, 1, 5*time.Second) {
		t.Fatal("write-budget run did not reach fake model")
	}
	before := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "running" && run.OwnerID != "" && run.FencingToken > 0
	})

	// The cluster fixture renews a 2s owner lease every ~667ms. Waiting 3s
	// crosses several renewals; none may update the durable ledger row.
	time.Sleep(3 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	after, err := requireLedger(t).runByInvocation(ctx, sessionID, invocationID)
	if err != nil {
		t.Fatalf("read run after lease renewals: %v", err)
	}
	if after.State != "running" || after.RunID != admitted.RunID {
		t.Fatalf("write-budget run changed identity/state: before=%#v after=%#v", before, after)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) ||
		after.OwnerID != before.OwnerID ||
		after.FencingToken != before.FencingToken ||
		after.LiveGeneration != before.LiveGeneration {
		t.Errorf("lease renewal wrote session_runs: before=%#v after=%#v", before, after)
	}

	globalFakeModel.Release(marker)
	mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
}

func TestSRDUR001AcceptedInputSurvivesOwnerCrashAsLedgerFact(t *testing.T) {
	if !envBool(crashEnv) {
		t.Skipf("set %s=1 to run the destructive owner-crash scenario", crashEnv)
	}
	fixture := requireFixture(t, false)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "owner-crash")
	marker := uniqueMarker("crash")
	invocationID := "invocation-" + marker
	text := directiveMode(marker, 1, 0, "block") + " accepted input survives owner crash"
	env := loadEnvironment()

	primary := mustDial(t, env.primaryURL, fixture)
	_, admitted := mustSendAndAccept(t, fixture, primary, sessionID, invocationID, text)
	if !globalFakeModel.WaitRequestCount(marker, 1, 5*time.Second) {
		t.Fatal("owner-crash run did not reach fake model")
	}
	running := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "running"
	})
	if running.RunID != admitted.RunID {
		t.Fatalf("SR-DUR-001: accepted/ledger run mismatch: accepted=%s ledger=%s", admitted.RunID, running.RunID)
	}

	if err := killAndRestartPrimary(env); err != nil {
		_ = primary.Close()
		t.Fatalf("kill and restart primary Server: %v", err)
	}
	_ = primary.Close()

	ledger := requireLedger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	surviving, err := ledger.waitRun(ctx, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "lost"
	})
	if err != nil {
		t.Fatalf("SR-DUR-001: accepted run did not converge to lost after owner crash: %v", err)
	}
	if err := surviving.validateIdentity(fixture.botID, sessionID, invocationID, marker); err != nil {
		t.Errorf("SR-DUR-001: durable admission invalid after crash: %v", err)
	}
	if surviving.ErrorCode == "" {
		t.Error("SR-DUR-001: lost run has no durable error_code")
	}

	secondary := mustDial(t, peerURL(env), fixture)
	defer closeWebSocket(secondary)
	if err := subscribeRuntime(secondary, sessionID); err != nil {
		t.Fatalf("subscribe after owner crash: %v", err)
	}
	snapshotEvents, err := readUntil(secondary, 5*time.Second, func(event wsEvent) bool {
		return event.Type == "runtime_snapshot" && eventRunID(event) == surviving.RunID
	})
	if err != nil {
		t.Fatalf("SR-DUR-001: no durable run snapshot after owner crash: %v; events=%#v", err, snapshotEvents)
	}
	if state := eventState(snapshotEvents[len(snapshotEvents)-1]); state != "lost" {
		t.Errorf("SR-DUR-001: post-crash snapshot state = %q, want lost", state)
	}
}

func TestSRDUR002TerminalReplayDoesNotDuplicateTurnOrOutput(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "terminal-replay")
	marker := uniqueMarker("terminal-replay")
	invocationID := "invocation-" + marker
	text := directive(marker, 4, 20) + " terminal replay"

	primary := mustDial(t, loadEnvironment().primaryURL, fixture)
	defer closeWebSocket(primary)
	mustSubscribeAndReadSnapshot(t, primary, sessionID)
	_, admitted := mustSendAndAccept(t, fixture, primary, sessionID, invocationID, text)
	mustReadRunTerminal(t, primary, admitted.RunID)
	terminal := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	before := assertTerminalHistory(t, terminal)

	secondary := mustDial(t, peerURL(loadEnvironment()), fixture)
	defer closeWebSocket(secondary)
	if err := sendChat(secondary, sessionID, invocationID, text); err != nil {
		t.Fatalf("replay terminal invocation: %v", err)
	}
	_, replay, err := readAccepted(secondary, invocationID, 5*time.Second)
	if err != nil {
		t.Fatalf("SR-DUR-002: terminal replay did not return original admission: %v", err)
	}
	if !replay.Duplicate || eventRunID(replay) != terminal.RunID || eventTurnID(replay) != terminal.TurnID {
		t.Errorf("SR-DUR-002: replay response = %#v, original=%#v", replay, terminal)
	}
	time.Sleep(200 * time.Millisecond)
	after := assertTerminalHistory(t, terminal)
	if after != before {
		t.Errorf("SR-DUR-002: terminal replay changed history: before=%#v after=%#v", before, after)
	}
	if count := globalFakeModel.RequestCount(marker); count != 1 {
		t.Errorf("SR-DUR-002: terminal replay model calls = %d, want 1", count)
	}
}

func TestSRDEC001DecisionPersistsRunAndTurnAcrossRestart(t *testing.T) {
	if !envBool(crashEnv) {
		t.Skipf("set %s=1 to run the destructive decision-restart scenario", crashEnv)
	}
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "decision-restart")
	marker := uniqueMarker("decision")
	invocationID := "invocation-" + marker
	text := directiveMode(marker, 2, 10, "ask_user") + " ask before continuing"
	env := loadEnvironment()

	primary := mustDial(t, env.primaryURL, fixture)
	_, admitted := mustSendAndAccept(t, fixture, primary, sessionID, invocationID, text)
	run := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "waiting_decision"
	})
	decision := mustPendingUserInput(t, run)

	if err := killAndRestartPrimary(env); err != nil {
		_ = primary.Close()
		t.Fatalf("kill and restart owner with pending decision: %v", err)
	}
	_ = primary.Close()

	statusCtx, statusCancel := context.WithTimeout(context.Background(), databaseTimeout)
	status, err := requireLedger(t).userInputStatus(statusCtx, decision.ID)
	statusCancel()
	if err != nil || status != "pending" {
		t.Fatalf("SR-DEC-001: decision after owner restart = %q, err=%v, want pending", status, err)
	}
	recovered := mustWaitRunState(t, sessionID, invocationID, func(candidate sessionRunRecord) bool {
		return candidate.State == "waiting_decision" && candidate.FencingToken > run.FencingToken
	})
	decision = mustPendingUserInput(t, recovered)

	answers, err := firstDecisionAnswer(decision.UIPayload)
	if err != nil {
		t.Fatalf("build answer from durable decision payload: %v", err)
	}
	secondary := mustDial(t, peerURL(env), fixture)
	defer closeWebSocket(secondary)
	controlID := "control-" + uniqueMarker("decision")
	if err := sendUserInputResponse(secondary, sessionID, admitted.RunID, decision.ID, controlID, answers); err != nil {
		t.Fatalf("answer decision through secondary: %v", err)
	}
	ackEvents, err := readUntil(secondary, 8*time.Second, func(event wsEvent) bool {
		return event.Type == "control_ack" && event.ControlID == controlID
	})
	if err != nil {
		t.Fatalf("SR-DEC-001: decision answer was not acknowledged: %v; events=%#v", err, ackEvents)
	}
	firstAck := ackEvents[len(ackEvents)-1]
	if !firstAck.Applied || firstAck.Control != "user_input_response" || eventRunID(firstAck) != admitted.RunID {
		t.Fatalf("SR-DEC-001: restarted-owner decision ack = %#v, want applied response", firstAck)
	}
	completed := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	assertDecisionRunCompletedOnce(t, completed, decision.ID, marker, "submitted")

	replayer := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(replayer)
	if err := sendUserInputResponse(replayer, sessionID, admitted.RunID, decision.ID, controlID, answers); err != nil {
		t.Fatalf("repeat decision after recovered run completed: %v", err)
	}
	retryEvents, retryErr := readUntil(replayer, 8*time.Second, func(event wsEvent) bool {
		return event.Type == "control_ack" && event.ControlID == controlID
	})
	if retryErr != nil {
		t.Fatalf("SR-DEC-001: terminal decision retry was not acknowledged: %v; events=%#v", retryErr, retryEvents)
	}
	assertSameControlAck(t, firstAck, retryEvents[len(retryEvents)-1])
}

func TestBackendLossMarksOldGenerationLostAndPreservesNewRuns(t *testing.T) {
	if !envBool(backendLossEnv) {
		t.Skipf("set %s=1 only against the isolated cluster acceptance Valkey", backendLossEnv)
	}
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "backend-loss")
	marker := uniqueMarker("backend-loss")
	invocationID := "invocation-" + marker
	text := directiveMode(marker, 3, 30, "partial_block") + " backend loss"
	env := loadEnvironment()

	primary := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(primary)
	mustSubscribeAndReadSnapshot(t, primary, sessionID)
	_, oldRun := mustSendAndAccept(t, fixture, primary, sessionID, invocationID, text)
	defer globalFakeModel.Release(marker)
	partialEvents, err := readUntil(primary, 5*time.Second, func(event wsEvent) bool {
		return eventsContainString([]wsEvent{event}, marker+"-chunk-02")
	})
	if err != nil {
		t.Fatalf("backend-loss run did not stream partial text before FLUSHDB: %v; events=%#v", err, partialEvents)
	}
	oldRunning := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "running" && run.LiveGeneration != ""
	})
	if err := flushAcceptanceBackend(context.Background(), env.redisURL); err != nil {
		t.Fatalf("flush isolated acceptance backend: %v", err)
	}

	time.Sleep(250 * time.Millisecond)
	ledger := requireLedger(t)
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 2*time.Second)
	stillRunning, err := ledger.runByInvocation(checkCtx, sessionID, invocationID)
	checkCancel()
	if err != nil {
		t.Fatalf("read run during backend loss grace: %v", err)
	}
	if stillRunning.State == "lost" {
		t.Error("backend loss marked run lost before backend_loss_grace elapsed")
	}

	newMarker := uniqueMarker("post-loss")
	newInvocation := "invocation-" + newMarker
	newText := directiveMode(newMarker, 1, 0, "block") + " after backend loss"
	secondary := mustDial(t, env.secondaryURL, fixture)
	defer closeWebSocket(secondary)
	if err := sendChat(secondary, sessionID, newInvocation, newText); err != nil {
		t.Fatalf("submit while stale generation is inside grace: %v", err)
	}
	busyEvents, _, busyErr := readRejected(
		secondary,
		newInvocation,
		string(apperror.CodeSessionBusy),
		5*time.Second,
	)
	if busyErr != nil {
		t.Fatalf("backend-loss grace did not preserve the active admission gate: %v; events=%#v", busyErr, busyEvents)
	}
	busyCtx, busyCancel := context.WithTimeout(context.Background(), databaseTimeout)
	count, countErr := ledger.runCount(busyCtx, sessionID, newInvocation)
	busyCancel()
	if countErr != nil || count != 0 {
		t.Errorf("backend-loss busy submission rows = %d, err=%v, want 0", count, countErr)
	}

	lost := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool {
		return run.State == "lost"
	})
	if lost.ErrorCode == "" {
		t.Error("backend-loss run has no durable error_code")
	}
	if lost.RunID != oldRun.RunID || lost.LiveGeneration != oldRunning.LiveGeneration {
		t.Errorf("backend-loss identity changed: before=%#v after=%#v", oldRunning, lost)
	}
	if err := subscribeRuntime(secondary, sessionID); err != nil {
		t.Fatalf("subscribe after backend loss: %v", err)
	}
	snapshotEvents, snapshotErr := readUntil(secondary, 5*time.Second, func(event wsEvent) bool {
		return event.Type == "runtime_snapshot" && eventRunID(event) == lost.RunID
	})
	if snapshotErr != nil {
		t.Fatalf("backend-loss snapshot did not expose lost run: %v; events=%#v", snapshotErr, snapshotEvents)
	}
	if eventsContainString(snapshotEvents, marker+"-chunk-") {
		t.Errorf("backend-loss snapshot replayed non-durable partial text: %#v", snapshotEvents)
	}
	historyCtx, historyCancel := context.WithTimeout(context.Background(), databaseTimeout)
	history, historyErr := ledger.historySummary(historyCtx, lost)
	historyCancel()
	if historyErr != nil {
		t.Fatalf("query lost run history: %v", historyErr)
	}
	if history.AssistantMessages != 0 {
		t.Errorf("lost run persisted partial assistant output: %#v", history)
	}
	globalFakeModel.Release(marker)
	if !globalFakeModel.WaitIdle(5 * time.Second) {
		t.Fatal("stale owner did not stop after backend-loss release")
	}

	_, newRun := mustSendAndAccept(
		t,
		fixture,
		secondary,
		sessionID,
		newInvocation,
		newText,
	)
	defer globalFakeModel.Release(newMarker)
	if !globalFakeModel.WaitRequestCount(newMarker, 1, 5*time.Second) {
		t.Fatal("post-loss current-generation run did not reach fake model")
	}
	time.Sleep(time.Second)
	currentCtx, currentCancel := context.WithTimeout(context.Background(), databaseTimeout)
	currentRun, currentErr := ledger.runByInvocation(currentCtx, sessionID, newInvocation)
	currentCancel()
	if currentErr != nil {
		t.Fatalf("read post-loss current-generation run: %v", currentErr)
	}
	if currentRun.State != "running" || currentRun.LiveGeneration == oldRunning.LiveGeneration {
		t.Errorf("current-generation run was swept with stale generation: old=%#v current=%#v", oldRunning, currentRun)
	}
	globalFakeModel.Release(newMarker)
	completed := mustWaitRunState(t, sessionID, newInvocation, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	if completed.LiveGeneration == "" || completed.LiveGeneration == oldRunning.LiveGeneration {
		t.Errorf("post-loss generation = %q, old = %q", completed.LiveGeneration, oldRunning.LiveGeneration)
	}
	if completed.RunID != newRun.RunID {
		t.Errorf("post-loss accepted run = %s, ledger = %s", newRun.RunID, completed.RunID)
	}

	primarySessionID := mustCreateSession(t, fixture, "backend-loss-generation-peer")
	primaryMarker := uniqueMarker("post-loss-primary")
	primaryInvocation := "invocation-" + primaryMarker
	primaryConnection := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(primaryConnection)
	_, primaryRun := mustSendAndAccept(
		t,
		fixture,
		primaryConnection,
		primarySessionID,
		primaryInvocation,
		directive(primaryMarker, 1, 0)+" verify shared generation",
	)
	primaryCompleted := mustWaitRunState(t, primarySessionID, primaryInvocation, func(run sessionRunRecord) bool {
		return run.State == "completed"
	})
	if primaryCompleted.RunID != primaryRun.RunID ||
		primaryCompleted.LiveGeneration != completed.LiveGeneration {
		t.Errorf(
			"Servers did not converge on one post-loss generation: secondary=%#v primary=%#v",
			completed,
			primaryCompleted,
		)
	}
}

func prepareFakeModel(t *testing.T) {
	t.Helper()
	if !globalFakeModel.WaitIdle(15 * time.Second) {
		t.Fatal("previous fake-model request did not become idle")
	}
	globalFakeModel.Reset()
}

func mustCreateSession(t *testing.T, fixture acceptanceFixture, scenario string) string {
	t.Helper()
	sessionID, err := fixture.api.createSession(fixture.botID, scenario)
	if err != nil {
		t.Fatalf("create %s session: %v", scenario, err)
	}
	return sessionID
}

func mustDial(t *testing.T, baseURL string, fixture acceptanceFixture) *websocket.Conn {
	t.Helper()
	connection, err := dialChatWebSocket(baseURL, fixture.api.token, fixture.botID)
	if err != nil {
		t.Fatalf("connect to WebSocket at %s: %v", baseURL, err)
	}
	return connection
}

func peerURL(env acceptanceEnvironment) string {
	if env.mode == "single" {
		return env.primaryURL
	}
	return env.secondaryURL
}

func mustSendAndAccept(
	t *testing.T,
	fixture acceptanceFixture,
	connection *websocket.Conn,
	sessionID string,
	invocationID string,
	text string,
) (wsEvent, sessionRunRecord) {
	t.Helper()
	if err := sendChat(connection, sessionID, invocationID, text); err != nil {
		t.Fatalf("send invocation %s: %v", invocationID, err)
	}
	events, accepted, err := readAccepted(connection, invocationID, eventTimeout)
	if err != nil {
		t.Fatalf("wait for durable run_accepted: %v; events=%#v", err, events)
	}
	if eventRunID(accepted) == "" || eventTurnID(accepted) == "" {
		t.Fatalf("run_accepted lacks run_id or turn_id: %#v", accepted)
	}
	if eventEpoch(accepted) == "" || eventSeq(accepted) <= 0 {
		t.Fatalf("run_accepted lacks an authoritative cursor: %#v", accepted)
	}
	if _, err := uuid.Parse(eventRunID(accepted)); err != nil {
		t.Fatalf("run_accepted run_id is not UUID: %q", eventRunID(accepted))
	}
	if _, err := uuid.Parse(eventTurnID(accepted)); err != nil {
		t.Fatalf("run_accepted turn_id is not UUID: %q", eventTurnID(accepted))
	}
	run := mustWaitRunState(t, sessionID, invocationID, nil)
	if err := run.validateIdentity(fixture.botID, sessionID, invocationID, markerFromText(text)); err != nil {
		t.Fatalf("validate durable admission: %v", err)
	}
	if run.RunID != eventRunID(accepted) || run.TurnID != eventTurnID(accepted) {
		t.Fatalf("run_accepted and ledger identity differ: event=%#v ledger=%#v", accepted, run)
	}
	return accepted, run
}

func mustWaitRunState(
	t *testing.T,
	sessionID string,
	invocationID string,
	predicate func(sessionRunRecord) bool,
) sessionRunRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	run, err := requireLedger(t).waitRun(ctx, sessionID, invocationID, predicate)
	if err != nil {
		t.Fatalf("wait for session_runs row: %v", err)
	}
	return run
}

func mustReadRunTerminal(t *testing.T, connection *websocket.Conn, runID string) ([]wsEvent, wsEvent) {
	t.Helper()
	events, err := readUntil(connection, eventTimeout, func(event wsEvent) bool {
		return eventRunID(event) == runID && isTerminal(event)
	})
	if err != nil {
		t.Fatalf("wait for terminal runtime event for %s: %v; events=%#v", runID, err, events)
	}
	return events, events[len(events)-1]
}

func mustSubscribeAndReadSnapshot(t *testing.T, connection *websocket.Conn, sessionID string) wsEvent {
	t.Helper()
	if err := subscribeRuntime(connection, sessionID); err != nil {
		t.Fatalf("subscribe to runtime: %v", err)
	}
	events, err := readUntil(connection, 5*time.Second, func(event wsEvent) bool {
		return event.Type == "runtime_snapshot" && event.SessionID == sessionID
	})
	if err != nil {
		t.Fatalf("read initial runtime snapshot: %v; events=%#v", err, events)
	}
	return events[len(events)-1]
}

func assertSnapshotIdentity(t *testing.T, snapshot wsEvent, run sessionRunRecord) {
	t.Helper()
	if eventRunID(snapshot) != run.RunID || eventTurnID(snapshot) != run.TurnID {
		t.Errorf("snapshot identity = run %q turn %q, ledger = run %q turn %q", eventRunID(snapshot), eventTurnID(snapshot), run.RunID, run.TurnID)
	}
}

func assertTerminalHistory(t *testing.T, run sessionRunRecord) historyRunSummary {
	t.Helper()
	if !run.terminal() {
		t.Fatalf("run %s state = %s, want terminal", run.RunID, run.State)
	}
	ctx, cancel := context.WithTimeout(context.Background(), databaseTimeout)
	defer cancel()
	summary, err := requireLedger(t).waitHistory(ctx, run)
	if err != nil {
		t.Fatalf("wait for terminal run-linked history: %v; summary=%#v", err, summary)
	}
	if summary.UserMessages != 1 {
		t.Errorf("run-linked user messages = %d, want 1", summary.UserMessages)
	}
	if summary.AssistantMessages == 0 {
		t.Error("run-linked history has no assistant output")
	}
	if summary.DistinctTurnIDs != 1 || summary.WrongTurnIDs != 0 {
		t.Errorf("run-linked history does not converge on turn %s: %#v", run.TurnID, summary)
	}
	return summary
}

func uniqueMarker(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), markerSequence.Add(1))
}

func directive(marker string, chunks, delayMS int) string {
	return fmt.Sprintf("[acceptance:%s chunks=%d delay_ms=%d]", marker, chunks, delayMS)
}

func directiveMode(marker string, chunks, delayMS int, mode string) string {
	return fmt.Sprintf("[acceptance:%s chunks=%d delay_ms=%d mode=%s]", marker, chunks, delayMS, mode)
}

func markerFromText(text string) string {
	return parseDirective(text).marker
}

func containsPartialText(events []wsEvent) bool {
	for _, event := range events {
		if isPartialText(event) {
			return true
		}
	}
	return false
}

func eventsContainString(events []wsEvent, text string) bool {
	for _, event := range events {
		if valueContainsString(event.Data, text) ||
			valueContainsString(event.Snapshot, text) ||
			valueContainsString(event.Delta, text) {
			return true
		}
	}
	return false
}

func assertOrderedRunEvents(t *testing.T, events []wsEvent, runID string) {
	t.Helper()
	epoch := ""
	lastSeq := int64(0)
	count := 0
	for _, event := range events {
		if eventRunID(event) != runID || event.Type != "runtime_delta" {
			continue
		}
		count++
		if eventEpoch(event) == "" || eventSeq(event) <= lastSeq {
			t.Errorf("runtime event is not ordered: previous seq=%d event=%#v", lastSeq, event)
			continue
		}
		if epoch == "" {
			epoch = eventEpoch(event)
		} else if eventEpoch(event) != epoch {
			t.Errorf("runtime event epoch changed from %q to %q: %#v", epoch, eventEpoch(event), event)
		}
		lastSeq = eventSeq(event)
	}
	if count < 2 {
		t.Errorf("observed %d ordered runtime deltas for run %s, want at least 2: %#v", count, runID, events)
	}
}

func firstDecisionAnswer(payload json.RawMessage) ([]map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, err
	}
	questions, _ := decoded["questions"].([]any)
	if len(questions) == 0 {
		return nil, errors.New("decision payload has no questions")
	}
	question, _ := questions[0].(map[string]any)
	questionID := stringValue(question["id"])
	options, _ := question["options"].([]any)
	if questionID == "" || len(options) == 0 {
		return nil, fmt.Errorf("decision question cannot be answered: %#v", question)
	}
	option, _ := options[0].(map[string]any)
	optionID := stringValue(option["id"])
	if optionID == "" {
		return nil, fmt.Errorf("decision option has no id: %#v", option)
	}
	return []map[string]any{{
		"question_id": questionID,
		"option_ids":  []string{optionID},
	}}, nil
}

func killAndRestartPrimary(env acceptanceEnvironment) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// The container name comes from an explicit opt-in acceptance variable and
	// is passed as one argv element rather than through a shell.
	kill := exec.CommandContext(ctx, "docker", "kill", "--signal=KILL", env.primaryContainer) //nolint:gosec
	if output, err := kill.CombinedOutput(); err != nil {
		return fmt.Errorf("docker kill: %w: %s", err, output)
	}
	start := exec.CommandContext(ctx, "docker", "start", env.primaryContainer) //nolint:gosec
	if output, err := start.CombinedOutput(); err != nil {
		return fmt.Errorf("docker start: %w: %s", err, output)
	}

	deadline := time.Now().Add(90 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodHead, env.primaryURL+"/health", nil)
		if requestErr != nil {
			return fmt.Errorf("build primary health request: %w", requestErr)
		}
		response, err := client.Do(request) //nolint:gosec // explicit acceptance-test Server URL
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("primary Server did not become healthy after restart")
}
