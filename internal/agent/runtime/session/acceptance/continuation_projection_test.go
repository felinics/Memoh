//go:build integration

package acceptance

import (
	"fmt"

	"testing"
)

func TestDecisionContinuationPreservesStreamBlocks(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)
	sessionID := mustCreateSession(t, fixture, "continuation-blocks")
	marker := uniqueMarker("continuation")
	invocationID := "invocation-" + marker
	conn := mustDial(t, loadEnvironment().primaryURL, fixture)
	defer closeWebSocket(conn)
	mustSubscribeAndReadSnapshot(t, conn, sessionID)
	_, admitted := mustSendAndAccept(t, fixture, conn, sessionID, invocationID, directiveMode(marker, 2, 10, "ask_user_prefix")+" ask before continuing")
	run := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool { return run.State == "waiting_decision" })
	decision := mustPendingUserInput(t, run)
	answers, err := firstDecisionAnswer(decision.UIPayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := sendUserInputResponse(conn, sessionID, admitted.RunID, decision.ID, "control-"+marker, answers); err != nil {
		t.Fatal(err)
	}
	events, _ := mustReadRunTerminal(t, conn, admitted.RunID)
	terminal := mustWaitRunState(t, sessionID, invocationID, func(run sessionRunRecord) bool { return run.State == "completed" })
	assertTerminalHistory(t, terminal)
	blocks := map[string]map[string]any{}
	for _, event := range events {
		for _, field := range []string{"message_appends", "message_upserts"} {
			values, _ := event.Delta[field].([]any)
			for _, value := range values {
				block, _ := value.(map[string]any)
				id := fmt.Sprint(block["id"])
				if field == "message_appends" && blocks[id] != nil {
					blocks[id]["content"] = stringValue(blocks[id]["content"]) + stringValue(block["content"])
				} else {
					blocks[id] = block
				}
			}
		}
	}
	before, after := 0, 0
	for _, block := range blocks {
		switch stringValue(block["content"]) {
		case marker + "-before-decision":
			before++
		case marker + "-chunk-00 " + marker + "-chunk-01":
			after++
		}
	}
	if before != 1 || after != 1 || len(blocks) != 3 {
		t.Fatalf("terminal projection lost or duplicated a block: %#v", blocks)
	}
	history, err := fixture.api.history(fixture.botID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !historyContainsRoleText(history, "assistant", marker+"-before-decision") || !historyContainsRoleText(history, "assistant", marker+"-chunk-01") {
		t.Fatalf("history does not match live output: %#v", history)
	}
	t.Logf("verified continuation: session=%s run=%s", sessionID, admitted.RunID)
}
