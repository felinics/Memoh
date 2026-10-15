//go:build integration

package acceptance

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/external-subagent.py
var externalSubagentScript string

func TestSREXT001ManagedExternalSubagentLifecycle(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)
	marker := uniqueMarker("external")
	scriptPath, artifactPath := "/data/"+marker+".py", "/data/"+marker+".jsonl"
	var ignored map[string]any
	if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/container/start", nil, &ignored, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/container/fs/write", map[string]any{"path": scriptPath, "content": externalSubagentScript}, &ignored, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	backendID := ""
	t.Cleanup(func() {
		if backendID != "" {
			if err := fixture.api.request(http.MethodDelete, "/bots/"+fixture.botID+"/agents/"+backendID, nil, &ignored, http.StatusOK, http.StatusNoContent); err != nil {
				t.Errorf("clean up external agent: %v", err)
			}
		}
		for _, path := range []string{scriptPath, artifactPath} {
			if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/container/fs/delete", map[string]any{"path": path}, &ignored, http.StatusOK); err != nil {
				t.Errorf("clean up fixture file: %v", err)
			}
		}
	})
	var agent map[string]any
	if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/agents", map[string]any{
		"name": marker, "runtime": "acp", "enabled": true,
		"metadata": map[string]any{"provider": "acp", "managed": map[string]any{"command": "/usr/bin/python3", "arguments": scriptPath + "\n" + artifactPath}},
	}, &agent, http.StatusCreated); err != nil {
		t.Fatal(err)
	}
	backendID = stringValue(agent["id"])
	parentID := mustCreateSession(t, fixture, "external-parent")
	worker := "external_worker"
	invoke := func(phase, task string) map[string]any {
		t.Helper()
		turnMarker := uniqueMarker("external-" + phase)
		text := fmt.Sprintf("[acceptance:%s mode=external_subagent phase=%s backend=%s worker=%s task=%s]", turnMarker, phase, backendID, worker, task)
		connection := mustDial(t, loadEnvironment().primaryURL, fixture)
		defer closeWebSocket(connection)
		mustSubscribeAndReadSnapshot(t, connection, parentID)
		_, admitted := mustSendAndAccept(t, fixture, connection, parentID, "invocation-"+turnMarker, text)
		events, terminal := mustReadRunTerminal(t, connection, admitted.RunID)
		if eventState(terminal) != "completed" {
			t.Fatalf("main %s failed: %#v", phase, terminal)
		}
		toolName := "send_message"
		if phase == "spawn" {
			toolName = "spawn_agent"
			catalog := externalToolOutput(t, events, "list_agent_backends")
			if !valueContainsString(catalog, backendID) {
				t.Fatalf("backend catalog omitted configured ACP instance: %#v", catalog)
			}
		}
		if phase == "stop" {
			toolName = "kill_background"
		}
		return externalToolOutput(t, events, toolName)
	}
	spawn := invoke("spawn", "none")
	childID := stringValue(spawn["session_id"])
	if childID == "" || spawn["status"] != "completed" || spawn["runtime_type"] != "acp_agent" || spawn["bot_agent_id"] != backendID || spawn["usage_known"] != false {
		t.Fatalf("spawn returned an invalid external result: %#v", spawn)
	}
	var storedParent, runtime, binding string
	var nativeConfigs int
	if err := requireLedger(t).pool.QueryRow(t.Context(), `SELECT parent_session_id::text, runtime_type, bot_agent_id::text,
		(SELECT count(*) FROM subagent_configs WHERE session_id=bot_sessions.id)
		FROM bot_sessions WHERE id=$1 AND bot_id=$2`, childID, fixture.botID).Scan(&storedParent, &runtime, &binding, &nativeConfigs); err != nil {
		t.Fatal(err)
	}
	if storedParent != parentID || runtime != "acp_agent" || binding != backendID || nativeConfigs != 0 {
		t.Fatalf("persisted child descriptor: parent=%s runtime=%s binding=%s native_configs=%d", storedParent, runtime, binding, nativeConfigs)
	}
	checkRun := func(result map[string]any, state string) sessionRunRecord {
		t.Helper()
		return mustWaitRunState(t, childID, "subagent:"+stringValue(result["task_id"]), func(run sessionRunRecord) bool { return run.State == state })
	}
	assertTerminalHistory(t, checkRun(spawn, "completed"))
	followup := invoke("followup", "none")
	if followup["session_id"] != childID || followup["status"] != "completed" {
		t.Fatalf("followup did not continue the same child: %#v", followup)
	}
	assertTerminalHistory(t, checkRun(followup, "completed"))
	rows := readExternalArtifact(t, fixture, artifactPath)
	if externalArtifactCount(rows, "complete") != 2 || rows[0]["pid"] != rows[2]["pid"] || rows[0]["session_id"] != rows[2]["session_id"] {
		t.Fatalf("first two turns did not execute serially on the warm child: %#v", rows)
	}
	if err := fixture.api.setToolApprovalRequired(fixture.botID, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.api.setToolApprovalRequired(fixture.botID, false); err != nil {
			t.Errorf("restore approval policy: %v", err)
		}
	})
	approvalTask := invoke("approval", "none")
	waiting := checkRun(approvalTask, "waiting_decision")
	decision := mustPendingToolApproval(t, waiting)
	if waiting.SessionID != childID || waiting.BotID != fixture.botID {
		t.Fatalf("approval was not owned by the child: %#v", waiting)
	}
	if err := fixture.api.approveTool(fixture.botID, decision.ID); err != nil {
		t.Fatal(err)
	}
	assertTerminalHistory(t, checkRun(approvalTask, "completed"))
	rows = readExternalArtifact(t, fixture, artifactPath)
	if externalArtifactCount(rows, "approval") != 1 || externalArtifactCount(rows, "complete") != 3 {
		t.Fatalf("approval did not produce exactly one effect: %#v", rows)
	}
	cancelTask := invoke("cancel", "none")
	checkRun(cancelTask, "running")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows = readExternalArtifact(t, fixture, artifactPath)
		if rows[len(rows)-1]["action"] == "cancel" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	invoke("stop", stringValue(cancelTask["task_id"]))
	assertTerminalHistory(t, checkRun(cancelTask, "aborted"))
	rows = readExternalArtifact(t, fixture, artifactPath)
	if externalArtifactCount(rows, "cancel_received") != 1 || externalArtifactCount(rows, "cancel_settled") != 1 || externalArtifactCount(rows, "unexpected_effect") != 0 || externalArtifactCount(rows, "complete") != 3 {
		t.Fatalf("cancellation did not stop the child effect: %#v", rows)
	}
	cancelPrompt, cancelSettled := -1, -1
	for index, row := range rows {
		if row["event"] == "prompt" && row["action"] == "cancel" {
			cancelPrompt = index
		}
		if row["event"] == "cancel_settled" {
			cancelSettled = index
		}
		if row["event"] == "cancel_received" || row["event"] == "cancel_settled" {
			if cancelPrompt < 0 || row["pid"] != rows[cancelPrompt]["pid"] || row["session_id"] != rows[cancelPrompt]["session_id"] {
				t.Fatalf("cancellation receipt did not belong to the running prompt: %#v", rows)
			}
		}
	}
	resumed := invoke("followup", "none")
	if resumed["session_id"] != childID || resumed["status"] != "completed" || !strings.Contains(stringValue(resumed["text"]), "count=4") {
		t.Fatalf("post-cancel followup did not complete the next child effect: %#v", resumed)
	}
	assertTerminalHistory(t, checkRun(resumed, "completed"))
	rows = readExternalArtifact(t, fixture, artifactPath)
	if cancelSettled < 0 || len(rows) <= cancelSettled+1 || rows[cancelSettled+1]["event"] != "prompt" || rows[cancelSettled+1]["action"] != "followup" || externalArtifactCount(rows, "complete") != 4 {
		t.Fatalf("followup did not wait for the cancellation receipt: %#v", rows)
	}
	failed := invoke("failure", "none")
	if failed["status"] != "failed" {
		t.Fatalf("partial external failure was reported as success: %#v", failed)
	}
	assertTerminalHistory(t, checkRun(failed, "failed"))
	history, err := fixture.api.history(fixture.botID, childID)
	if err != nil || !historyContainsRoleText(history, "assistant", "external-subagent-partial-failure") {
		t.Fatalf("partial failure transcript was lost: %#v, %v", history, err)
	}
	rows = readExternalArtifact(t, fixture, artifactPath)
	failures := 0
	for _, row := range rows {
		if row["event"] == "prompt" && row["action"] == "failure" {
			failures++
		}
	}
	if failures != 1 || externalArtifactCount(rows, "complete") != 4 {
		t.Fatalf("external failure replayed or changed completed effects: failures=%d records=%#v", failures, rows)
	}
	t.Logf("parent=%s child=%s backend=%s real workspace effects=%d", parentID, childID, backendID, externalArtifactCount(rows, "complete"))
	encoded, _ := json.Marshal(rows)
	t.Logf("workspace receipts: %s", encoded)
}

func externalToolOutput(t *testing.T, events []wsEvent, name string) map[string]any {
	t.Helper()
	for index := len(events) - 1; index >= 0; index-- {
		for _, raw := range objectList(events[index].Delta["message_upserts"]) {
			if raw["name"] == name {
				if output, ok := raw["output"].(map[string]any); ok {
					return output
				}
				if output, ok := raw["output"]; ok {
					t.Fatalf("%s returned non-object result: %v", name, output)
				}
			}
		}
	}
	t.Fatalf("no %s output in %d runtime events", name, len(events))
	return nil
}

func readExternalArtifact(t *testing.T, fixture acceptanceFixture, path string) []map[string]any {
	t.Helper()
	var response map[string]any
	if err := fixture.api.request(http.MethodGet, "/bots/"+fixture.botID+"/container/fs/read?path="+url.QueryEscape(path), nil, &response, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stringValue(response["content"])), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func externalArtifactCount(rows []map[string]any, event string) int {
	count := 0
	for _, row := range rows {
		if row["event"] == event {
			count++
		}
	}
	return count
}

func writeExternalSubagentCall(writer http.ResponseWriter, flusher http.Flusher, requestID string, payload map[string]any) (bool, error) {
	options := map[string]string{}
	for _, match := range directiveOption.FindAllStringSubmatch(latestUserText(payload), -1) {
		options[match[1]] = match[2]
	}
	messages, _ := payload["messages"].([]any)
	called := map[string]bool{}
	for index := len(messages) - 1; index >= 0; index-- {
		message, _ := messages[index].(map[string]any)
		if message["role"] == "user" {
			break
		}
		for _, raw := range objectList(message["tool_calls"]) {
			function, _ := raw["function"].(map[string]any)
			called[stringValue(function["name"])] = true
		}
	}
	phase, name := options["phase"], "send_message"
	args := map[string]any{"id": options["worker"], "message": "external-subagent-action=" + phase, "run_in_background": phase == "approval" || phase == "cancel" || phase == "hold" || phase == "queued"}
	switch {
	case phase == "spawn" && !called["list_agent_backends"]:
		name, args = "list_agent_backends", map[string]any{}
	case phase == "spawn_hold" || phase == "spawn_hold_escape":
		name, args = "spawn_agent", map[string]any{"id": options["worker"], "bot_agent_id": options["backend"], "task": "external-subagent-action=" + strings.TrimPrefix(phase, "spawn_"), "run_in_background": true}
	case phase == "spawn" || phase == "capacity_spawn":
		name, args = "spawn_agent", map[string]any{"id": options["worker"], "bot_agent_id": options["backend"], "task": "external-subagent-action=initial", "run_in_background": false}
	case phase == "stop":
		name, args = "kill_background", map[string]any{"task_id": options["task"]}
	case phase == "list":
		name, args = "list_agents", map[string]any{}
	}
	if called[name] {
		return false, nil
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return true, err
	}
	reason := "tool_calls"
	if err := writeSSE(writer, flusher, completionChunk(requestID, map[string]any{"tool_calls": []map[string]any{{"index": 0, "id": requestID + "-tool", "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}}}, nil)); err != nil {
		return true, err
	}
	if err := writeSSE(writer, flusher, completionChunk(requestID, map[string]any{}, &reason)); err != nil {
		return true, err
	}
	_, err = writer.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
	return true, err
}
