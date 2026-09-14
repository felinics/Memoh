//go:build integration

package acceptance

import (
	"fmt"
	"testing"
)

func TestBinaryToolOutputPersistsAndContinues(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)
	sessionID := mustCreateSession(t, fixture, "binary-output")
	marker := uniqueMarker("binary")
	invocationID := "invocation-" + marker
	connection := mustDial(t, loadEnvironment().primaryURL, fixture)
	defer closeWebSocket(connection)
	mustSubscribeAndReadSnapshot(t, connection, sessionID)
	_, admitted := mustSendAndAccept(t, fixture, connection, sessionID, invocationID, fmt.Sprintf("[acceptance:%s mode=binary_output chunks=2] Read binary output", marker))
	events, _ := mustReadRunTerminal(t, connection, admitted.RunID)
	terminal := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool { return run.State == "completed" })
	assertTerminalHistory(t, terminal)
	if count := globalFakeModel.RequestCount(marker); count != 2 {
		t.Fatalf("model requests = %d, want tool result followed by final answer", count)
	}
	history, err := fixture.api.history(fixture.botID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !valueContainsString(history, `SQLite format 3\x00中文`) {
		t.Fatalf("binary preview missing in HTTP history: %#v", history)
	}
	if !eventsContainString(events, marker+"-chunk-01") || !historyContainsRoleText(history, "assistant", marker+"-chunk-01") {
		t.Fatal("run did not continue after binary tool output")
	}
	t.Logf("verified real exec + WebSocket + HTTP history: bot=%s session=%s run=%s", fixture.botID, sessionID, admitted.RunID)
}
