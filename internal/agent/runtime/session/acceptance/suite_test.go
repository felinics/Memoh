//go:build integration

package acceptance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var markerSequence atomic.Uint64

func TestSRBASE001BaselineCompletesAndPersists(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "baseline")
	marker := uniqueMarker("baseline")
	streamID := "stream-" + marker
	text := directive(marker, 3, 30) + " baseline"

	connection := mustDial(t, loadEnvironment().primaryURL, fixture)
	defer closeWebSocket(connection)
	if err := sendChat(connection, sessionID, streamID, streamID, text); err != nil {
		t.Fatalf("send baseline message: %v", err)
	}
	events, err := readUntil(connection, 20*time.Second, isTerminal)
	if err != nil {
		t.Fatalf("read baseline stream: %v; events=%#v", err, events)
	}
	if !hasEvent(events, "start") {
		t.Errorf("baseline stream did not emit start: %#v", events)
	}
	if !hasEvent(events, "end") {
		t.Errorf("baseline stream did not emit end: %#v", events)
	}
	if !containsPartialText(events) {
		t.Errorf("baseline stream did not emit text: %#v", events)
	}
	if !globalFakeModel.WaitRequestCount(marker, 1, 5*time.Second) ||
		!globalFakeModel.WaitIdle(10*time.Second) {
		t.Fatal("fake model did not complete the baseline request")
	}
	if count := globalFakeModel.RequestCount(marker); count != 1 {
		t.Errorf("model executions = %d, want 1", count)
	}

	history, err := fixture.api.history(fixture.botID, sessionID)
	if err != nil {
		t.Fatalf("query baseline history: %v", err)
	}
	if count := countUserText(history, text); count != 1 {
		t.Errorf("persisted matching user turns = %d, want 1; history=%#v", count, history)
	}
	if !hasAssistantTurn(history) {
		t.Errorf("history has no assistant turn: %#v", history)
	}
}

func TestSROBS001ReconnectReceivesAuthoritativeSnapshot(t *testing.T) {
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "reconnect-snapshot")
	marker := uniqueMarker("snapshot")
	streamID := "stream-" + marker
	text := directive(marker, 50, 100) + " reconnect snapshot"

	primary := mustDial(t, loadEnvironment().primaryURL, fixture)
	if err := sendChat(primary, sessionID, streamID, streamID, text); err != nil {
		_ = primary.Close()
		t.Fatalf("send long-running message: %v", err)
	}
	events, err := readUntil(primary, 10*time.Second, isPartialText)
	if err != nil {
		_ = primary.Close()
		t.Fatalf("wait for partial output: %v; events=%#v", err, events)
	}
	_ = primary.Close()
	defer globalFakeModel.WaitIdle(10 * time.Second)

	secondary := mustDial(t, loadEnvironment().secondaryURL, fixture)
	defer closeWebSocket(secondary)
	if err := subscribeRuntime(secondary, sessionID); err != nil {
		t.Fatalf("subscribe to runtime after reconnect: %v", err)
	}
	snapshotEvents, err := readUntil(secondary, 3*time.Second, func(event wsEvent) bool {
		return event.Type == "runtime_snapshot" && event.SessionID == sessionID
	})
	if err != nil {
		t.Fatalf("SR-OBS-001: reconnect did not return an authoritative runtime_snapshot: %v; events=%#v", err, snapshotEvents)
	}
}

func TestSRCTL001ReconnectAbortReachesOwner(t *testing.T) {
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "reconnect-abort")
	marker := uniqueMarker("abort")
	streamID := "stream-" + marker
	text := directive(marker, 80, 100) + " reconnect abort"

	primary := mustDial(t, loadEnvironment().primaryURL, fixture)
	if err := sendChat(primary, sessionID, streamID, streamID, text); err != nil {
		_ = primary.Close()
		t.Fatalf("send long-running message: %v", err)
	}
	events, err := readUntil(primary, 10*time.Second, isPartialText)
	if err != nil {
		_ = primary.Close()
		t.Fatalf("wait for partial output: %v; events=%#v", err, events)
	}
	_ = primary.Close()

	secondary := mustDial(t, loadEnvironment().secondaryURL, fixture)
	defer closeWebSocket(secondary)
	if err := subscribeRuntime(secondary, sessionID); err != nil {
		t.Fatalf("subscribe to runtime: %v", err)
	}
	if err := sendAbort(secondary, sessionID, streamID); err != nil {
		t.Fatalf("send abort through secondary Server: %v", err)
	}
	ackEvents, ackErr := readUntil(secondary, 4*time.Second, func(event wsEvent) bool {
		return event.Type == "control_ack" &&
			event.SessionID == sessionID &&
			event.StreamID == streamID
	})
	if ackErr != nil {
		t.Errorf("SR-CTL-001: abort did not receive a cross-instance control_ack: %v; events=%#v", ackErr, ackEvents)
	}
	if !globalFakeModel.WaitDisconnected(marker, 5*time.Second) {
		t.Errorf("SR-CTL-001: owner did not cancel the upstream model request")
	}
	if !globalFakeModel.WaitIdle(12 * time.Second) {
		t.Error("upstream model request remained active after abort")
	}
}

func TestSRADM001DuplicateInvocationExecutesOnceAcrossInstances(t *testing.T) {
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "duplicate-invocation")
	marker := uniqueMarker("duplicate")
	streamID := "stream-" + marker
	text := directive(marker, 12, 80) + " duplicate invocation"
	env := loadEnvironment()

	primary := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(primary)
	secondary := mustDial(t, env.secondaryURL, fixture)
	defer closeWebSocket(secondary)

	start := make(chan struct{})
	errors := make(chan error, 2)
	go func() {
		<-start
		errors <- sendChat(primary, sessionID, streamID, streamID, text)
	}()
	go func() {
		<-start
		errors <- sendChat(secondary, sessionID, streamID, streamID, text)
	}()
	close(start)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatalf("send duplicate invocation: %v", err)
		}
	}

	done := make(chan struct{}, 2)
	go func() {
		_, _ = readUntil(primary, 15*time.Second, isTerminal)
		done <- struct{}{}
	}()
	go func() {
		_, _ = readUntil(secondary, 15*time.Second, isTerminal)
		done <- struct{}{}
	}()
	<-done
	<-done

	if !globalFakeModel.WaitRequestCount(marker, 1, 5*time.Second) {
		t.Fatal("duplicate invocation never reached the fake model")
	}
	if !globalFakeModel.WaitIdle(15 * time.Second) {
		t.Fatal("duplicate invocation model request did not finish")
	}
	if count := globalFakeModel.RequestCount(marker); count != 1 {
		t.Errorf("SR-ADM-001: model executions for one invocation = %d, want 1", count)
	}

	history, count, err := waitForUserText(fixture, sessionID, text, 5*time.Second)
	if err != nil {
		t.Fatalf("query duplicate invocation history: %v", err)
	}
	if count != 1 {
		t.Errorf("SR-ADM-001: persisted matching user turns = %d, want 1; history=%#v", count, history)
	}
}

func TestSROWN001SameSessionDoesNotExecuteRunsConcurrently(t *testing.T) {
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "same-session-owner")
	firstMarker := uniqueMarker("owner-a")
	secondMarker := uniqueMarker("owner-b")
	firstStream := "stream-" + firstMarker
	secondStream := "stream-" + secondMarker
	env := loadEnvironment()

	primary := mustDial(t, env.primaryURL, fixture)
	defer closeWebSocket(primary)
	secondary := mustDial(t, env.secondaryURL, fixture)
	defer closeWebSocket(secondary)

	start := make(chan struct{})
	sendErrors := make(chan error, 2)
	go func() {
		<-start
		sendErrors <- sendChat(
			primary,
			sessionID,
			firstStream,
			firstStream,
			directive(firstMarker, 15, 100)+" first owner candidate",
		)
	}()
	go func() {
		<-start
		sendErrors <- sendChat(
			secondary,
			sessionID,
			secondStream,
			secondStream,
			directive(secondMarker, 15, 100)+" second owner candidate",
		)
	}()
	close(start)
	for range 2 {
		if err := <-sendErrors; err != nil {
			t.Fatalf("send owner candidate: %v", err)
		}
	}

	var firstEvents, secondEvents []wsEvent
	var firstErr, secondErr error
	done := make(chan struct{}, 2)
	go func() {
		firstEvents, firstErr = readUntil(primary, 15*time.Second, isTerminal)
		done <- struct{}{}
	}()
	go func() {
		secondEvents, secondErr = readUntil(secondary, 15*time.Second, isTerminal)
		done <- struct{}{}
	}()
	<-done
	<-done
	if firstErr != nil && !isTimeout(firstErr) {
		t.Errorf("read first owner candidate: %v; events=%#v", firstErr, firstEvents)
	}
	if secondErr != nil && !isTimeout(secondErr) {
		t.Errorf("read second owner candidate: %v; events=%#v", secondErr, secondEvents)
	}
	if !globalFakeModel.WaitIdle(10 * time.Second) {
		t.Error("same-session model requests did not finish")
	}
	t.Logf(
		"first_requests=%d second_requests=%d max_active=%d first_event_types=%v second_event_types=%v first_err=%v second_err=%v",
		globalFakeModel.RequestCount(firstMarker),
		globalFakeModel.RequestCount(secondMarker),
		globalFakeModel.MaxActive(),
		eventTypes(firstEvents),
		eventTypes(secondEvents),
		firstErr,
		secondErr,
	)
	if maxActive := globalFakeModel.MaxActive(); maxActive > 1 {
		t.Errorf("SR-OWN-001: observed %d concurrent model executions for one session, want at most 1", maxActive)
	}
}

func TestSRDUR001AcceptedInputSurvivesOwnerCrash(t *testing.T) {
	if !envBool(crashEnv) {
		t.Skipf("set %s=1 to run the destructive owner-crash scenario", crashEnv)
	}
	fixture := requireFixture(t, true)
	prepareFakeModel(t)

	sessionID := mustCreateSession(t, fixture, "owner-crash")
	marker := uniqueMarker("crash")
	streamID := "stream-" + marker
	text := directive(marker, 200, 100) + " accepted input survives owner crash"
	env := loadEnvironment()

	primary := mustDial(t, env.primaryURL, fixture)
	if err := sendChat(primary, sessionID, streamID, streamID, text); err != nil {
		_ = primary.Close()
		t.Fatalf("send crash scenario message: %v", err)
	}
	// Current main has no durable accepted acknowledgement. A partial response
	// is the earliest public evidence that the Server started this input. Once
	// durable admission exists, this must wait for accepted before SIGKILL.
	events, err := readUntil(primary, 10*time.Second, isPartialText)
	if err != nil {
		_ = primary.Close()
		t.Fatalf("wait for accepted execution before crash: %v; events=%#v", err, events)
	}
	if err := killAndRestartPrimary(env); err != nil {
		_ = primary.Close()
		t.Fatalf("kill and restart primary Server: %v", err)
	}
	_ = primary.Close()

	history, err := fixture.api.history(fixture.botID, sessionID)
	if err != nil {
		t.Fatalf("query history after owner restart: %v", err)
	}
	if count := countUserText(history, text); count != 1 {
		t.Errorf("SR-DUR-001: persisted accepted input count after crash = %d, want 1; history=%#v", count, history)
	}

	secondary := mustDial(t, env.secondaryURL, fixture)
	defer closeWebSocket(secondary)
	if err := subscribeRuntime(secondary, sessionID); err != nil {
		t.Fatalf("subscribe after owner crash: %v", err)
	}
	snapshotEvents, err := readUntil(secondary, 4*time.Second, func(event wsEvent) bool {
		return event.Type == "runtime_snapshot" && event.SessionID == sessionID
	})
	if err != nil {
		t.Errorf("SR-DUR-001: no durable run snapshot after owner crash: %v; events=%#v", err, snapshotEvents)
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

func uniqueMarker(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), markerSequence.Add(1))
}

func directive(marker string, chunks, delayMS int) string {
	return fmt.Sprintf("[acceptance:%s chunks=%d delay_ms=%d]", marker, chunks, delayMS)
}

func containsPartialText(events []wsEvent) bool {
	for _, event := range events {
		if isPartialText(event) {
			return true
		}
	}
	return false
}

func eventTypes(events []wsEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		if len(types) == 0 || types[len(types)-1] != event.Type {
			types = append(types, event.Type)
		}
	}
	return types
}

func waitForUserText(
	fixture acceptanceFixture,
	sessionID string,
	text string,
	timeout time.Duration,
) (map[string]any, int, error) {
	deadline := time.Now().Add(timeout)
	var last map[string]any
	for time.Now().Before(deadline) {
		history, err := fixture.api.history(fixture.botID, sessionID)
		if err != nil {
			return nil, 0, err
		}
		last = history
		count := countUserText(history, text)
		if count > 0 {
			return history, count, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last, countUserText(last, text), nil
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
