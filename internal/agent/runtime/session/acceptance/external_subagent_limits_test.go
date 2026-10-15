//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

func TestSREXT002ExternalSubagentCapacityAndQueueRecovery(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)
	probe := requireLedger(t)
	marker := uniqueMarker("external-capacity")
	scriptPath, artifactPath := "/data/"+marker+".py", "/data/"+marker+".jsonl"
	var response map[string]any
	var settings, model map[string]any
	if err := fixture.api.request(http.MethodGet, "/bots/"+fixture.botID+"/settings", nil, &settings, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	modelID := stringValue(settings["chat_model_id"])
	if err := fixture.api.request(http.MethodGet, "/models/"+modelID, nil, &model, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	model["config"] = map[string]any{"compatibilities": []string{"tool-call"}, "context_window": 262144, "thinking_mode": "none"}
	if err := fixture.api.request(http.MethodPut, "/models/"+modelID, model, &response, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/container/start", nil, &response, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	backendID := ""
	var parents, children, holds, queued []string
	t.Cleanup(func() {
		var artifact map[string]any
		if err := fixture.api.request(http.MethodGet, "/bots/"+fixture.botID+"/container/fs/read?path="+url.QueryEscape(artifactPath), nil, &artifact, http.StatusOK); err != nil {
			t.Logf("capture capacity artifact failed: %v", err)
		} else {
			t.Logf("capacity bot=%s parents=%v children=%v holds=%v queued=%v receipts=%s", fixture.botID, parents, children, holds, queued, stringValue(artifact["content"]))
		}
		if backendID != "" {
			if err := fixture.api.request(http.MethodDelete, "/bots/"+fixture.botID+"/agents/"+backendID, nil, &response, http.StatusOK, http.StatusNoContent); err != nil {
				t.Errorf("close capacity agent processes: %v", err)
			}
		}
		for _, path := range []string{scriptPath, artifactPath} {
			if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/container/fs/delete", map[string]any{"path": path}, &response, http.StatusOK); err != nil {
				t.Errorf("remove capacity fixture: %v", err)
			}
		}
		if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/container/stop", nil, &response, http.StatusOK); err != nil {
			t.Errorf("stop capacity workspace: %v", err)
		}
		if err := fixture.api.request(http.MethodGet, "/bots/"+fixture.botID+"/container", nil, &response, http.StatusOK); err != nil || response["task_running"] != false || response["status"] != "stopped" {
			t.Errorf("capacity workspace cleanup was not confirmed: status=%v err=%v", response, err)
		}
	})
	if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/container/fs/write", map[string]any{"path": scriptPath, "content": externalSubagentScript}, &response, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/agents", map[string]any{
		"name": marker, "runtime": "acp", "enabled": true,
		"metadata": map[string]any{"provider": "acp", "managed": map[string]any{"command": "/usr/bin/python3", "arguments": scriptPath + "\n" + artifactPath}},
	}, &response, http.StatusCreated); err != nil {
		t.Fatal(err)
	}
	backendID = stringValue(response["id"])
	parents = []string{mustCreateSession(t, fixture, marker+"-parent-a"), mustCreateSession(t, fixture, marker+"-parent-b")}
	invoke := func(parentID, phase, worker, taskID string) (map[string]any, bool) {
		t.Helper()
		turnMarker := uniqueMarker("capacity-" + phase)
		text := fmt.Sprintf("[acceptance:%s mode=external_subagent phase=%s backend=%s worker=%s task=%s]", turnMarker, phase, backendID, worker, taskID)
		connection := mustDial(t, loadEnvironment().primaryURL, fixture)
		defer closeWebSocket(connection)
		mustSubscribeAndReadSnapshot(t, connection, parentID)
		_, admitted := mustSendAndAccept(t, fixture, connection, parentID, "invocation-"+turnMarker, text)
		_, terminal := mustReadRunTerminal(t, connection, admitted.RunID)
		if eventState(terminal) != "completed" {
			t.Fatalf("capacity parent %s failed: %#v", phase, terminal)
		}
		name := "send_message"
		switch phase {
		case "spawn", "capacity_spawn":
			name = "spawn_agent"
		case "stop":
			name = "kill_background"
		case "list":
			name = "list_agents"
		}
		queryCtx, queryCancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer queryCancel()
		rows, err := probe.runHistory(queryCtx, admitted.RunID)
		if err != nil {
			t.Fatal(err)
		}
		calls := map[string]bool{}
		for _, row := range rows {
			message := historyfrag.StoredModelMessageToSDKMessage(historyfrag.DecodeStoredModelMessage(nil, row.ID, row.Role, row.Content))
			for _, part := range message.Content {
				switch part := part.(type) {
				case sdk.ToolCallPart:
					calls[part.ToolCallID] = part.ToolName == name
				case sdk.ToolResultPart:
					if calls[part.ToolCallID] {
						value := toolexec.OutputValue(part.Result)
						result, _ := value.(map[string]any)
						if part.IsError {
							t.Logf("expected boundary refusal phase=%s output=%v", phase, value)
							result = map[string]any{"error": value}
						}
						return result, part.IsError
					}
				}
			}
		}
		t.Fatalf("no persisted %s result", name)
		return nil, false
	}
	success := func(parentID, phase, worker, taskID string) map[string]any {
		t.Helper()
		result, failed := invoke(parentID, phase, worker, taskID)
		if failed || result == nil {
			t.Fatalf("capacity action %s failed: %v", phase, result)
		}
		return result
	}
	cancelledTasks := map[string]bool{}
	trackTask := func(parentID, worker, taskID, childID string) {
		t.Cleanup(func() {
			if !cancelledTasks[taskID] {
				invoke(parentID, "stop", worker, taskID)
				if childID != "" {
					mustWaitRunState(t, childID, "subagent:"+taskID, func(run sessionRunRecord) bool { return run.terminal() })
				}
			}
		})
	}
	countRuns := func(activeOnly bool) int {
		t.Helper()
		query := `SELECT count(*) FROM session_runs r JOIN bot_sessions s ON s.id=r.session_id WHERE s.bot_id=$1 AND s.parent_session_id=ANY($2::uuid[])`
		if activeOnly {
			query += ` AND r.state NOT IN ('completed','failed','aborted','lost')`
		}
		var count int
		if err := probe.pool.QueryRow(t.Context(), query, fixture.botID, parents).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	children = make([]string, 3)
	owners := []string{parents[0], parents[0], parents[1]}
	for i := range children {
		spawn := success(owners[i], "spawn", fmt.Sprintf("worker%d", i), "none")
		children[i] = stringValue(spawn["session_id"])
		if spawn["status"] != "completed" || children[i] == "" {
			t.Fatalf("warm child was not created: %v", spawn)
		}
	}
	holds = make([]string, 3)
	for i := range children {
		hold := success(owners[i], "hold", fmt.Sprintf("worker%d", i), "none")
		holds[i] = stringValue(hold["task_id"])
		if holds[i] != "" {
			trackTask(owners[i], fmt.Sprintf("worker%d", i), holds[i], children[i])
		}
		if hold["status"] != "background_started" || holds[i] == "" {
			t.Fatalf("hold did not start in background: %v", hold)
		}
		mustWaitRunState(t, children[i], "subagent:"+holds[i], func(run sessionRunRecord) bool { return run.State == "running" })
	}
	var holdReceipts []map[string]any
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		holdReceipts = nil
		for _, row := range readExternalArtifact(t, fixture, artifactPath) {
			if row["event"] == "prompt" && row["action"] == "hold" {
				holdReceipts = append(holdReceipts, row)
			}
		}
		if len(holdReceipts) == 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	holdPIDs, holdSessions := map[any]bool{}, map[any]bool{}
	for _, row := range holdReceipts {
		holdPIDs[row["pid"]], holdSessions[row["session_id"]] = true, true
	}
	if len(holdReceipts) != 3 || len(holdPIDs) != 3 || len(holdSessions) != 3 {
		t.Fatalf("three independent processes did not receive holds: %v", holdReceipts)
	}
	if active := countRuns(true); active != 3 {
		t.Fatalf("durable active child count=%d, want 3 across two parents", active)
	}
	beforeRuns := countRuns(false)
	if result, refused := invoke(parents[0], "capacity_spawn", "overflow", "none"); !refused || !strings.Contains(fmt.Sprint(result["error"]), "external subagent limit reached: at most 3 concurrent children") {
		t.Fatalf("fourth external execution did not hit the concurrency limit: %v", result)
	}
	if countRuns(false) != beforeRuns || countRuns(true) != 3 {
		t.Fatal("concurrency refusal admitted a run or displaced existing work")
	}
	seenTasks := map[string]bool{}
	for range 16 {
		result := success(parents[0], "queued", "worker0", "none")
		if result["status"] != "queued" || stringValue(result["task_id"]) == "" {
			t.Fatalf("pending followup was not queued: %v", result)
		}
		taskID := stringValue(result["task_id"])
		if seenTasks[taskID] {
			t.Fatalf("queued task identity reused: %s", taskID)
		}
		seenTasks[taskID] = true
		queued = append(queued, taskID)
		trackTask(parents[0], "worker0", taskID, "")
	}
	if result, refused := invoke(parents[0], "queued", "worker0", "none"); !refused || !strings.Contains(fmt.Sprint(result["error"]), "queue_capacity_exceeded") {
		t.Fatalf("seventeenth pending followup did not hit the queue limit: %v", result)
	}
	if countRuns(false) != beforeRuns || countRuns(true) != 3 {
		t.Fatal("queued or refused work executed before the active child settled")
	}
	for _, taskID := range queued {
		success(parents[0], "stop", "worker0", taskID)
		cancelledTasks[taskID] = true
	}
	listing := success(parents[0], "list", "worker0", "none")
	found := false
	for _, agent := range objectList(listing["agents"]) {
		if agent["agent_id"] == "worker0" {
			found = true
			if agent["queued_count"] != float64(0) {
				t.Fatalf("cancelled pending work remains queued: %v", agent)
			}
		}
	}
	if !found {
		t.Fatalf("controlled child missing from listing: %v", listing)
	}
	for i, taskID := range holds {
		success(owners[i], "stop", fmt.Sprintf("worker%d", i), taskID)
		assertTerminalHistory(t, mustWaitRunState(t, children[i], "subagent:"+taskID, func(run sessionRunRecord) bool { return run.State == "aborted" }))
		cancelledTasks[taskID] = true
	}
	rows := readExternalArtifact(t, fixture, artifactPath)
	if externalArtifactCount(rows, "cancel_settled") != 3 || externalArtifactCount(rows, "unexpected_effect") != 0 || externalArtifactCount(rows, "complete") != 3 || countRuns(true) != 0 || countRuns(false) != beforeRuns {
		t.Fatalf("cancellation did not settle exact running work: receipts=%v", rows)
	}
	for _, hold := range holdReceipts {
		for _, event := range []string{"cancel_received", "cancel_settled"} {
			count := 0
			for _, row := range rows {
				if row["event"] == event && row["pid"] == hold["pid"] && row["session_id"] == hold["session_id"] {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("hold %v has %d %s receipts", hold, count, event)
			}
		}
	}
	pids := map[any]bool{}
	for _, row := range rows {
		if row["event"] == "prompt" {
			if row["action"] == "queued" {
				t.Fatal("cancelled queued work produced an actual prompt")
			}
			pids[row["pid"]] = true
		}
	}
	if len(pids) != 3 {
		t.Fatalf("observed %d processes, want three independent child processes", len(pids))
	}
	for _, action := range []struct{ phase, worker string }{{"followup", "overflow"}, {"capacity_spawn", "released"}, {"followup", "worker0"}} {
		result := success(parents[0], action.phase, action.worker, "none")
		if result["status"] != "completed" || !strings.Contains(stringValue(result["text"]), "external-subagent-complete-") {
			t.Fatalf("released capacity did not finish legitimate work: %v", result)
		}
		if action.worker == "worker0" && result["session_id"] != children[0] {
			t.Fatal("post-release followup changed child identity")
		}
		assertTerminalHistory(t, mustWaitRunState(t, stringValue(result["session_id"]), "subagent:"+stringValue(result["task_id"]), func(run sessionRunRecord) bool { return run.State == "completed" }))
	}
	rows = readExternalArtifact(t, fixture, artifactPath)
	for _, row := range rows {
		if row["event"] == "prompt" && row["action"] == "queued" {
			t.Fatal("cancelled queued work was dispatched after capacity was released")
		}
	}
	if externalArtifactCount(rows, "complete") != 6 || externalArtifactCount(rows, "unexpected_effect") != 0 || countRuns(true) != 0 || countRuns(false) != beforeRuns+3 {
		t.Fatalf("post-release business effects mismatch: %v", rows)
	}
	encoded, _ := json.Marshal(rows)
	t.Logf("capacity parent IDs=%v child IDs=%v queued=%d receipts=%s", parents, children, len(queued), encoded)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
	defer cancel()
	var active, unresolved, pending int
	if err := probe.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM session_runs WHERE bot_id=$1 AND state NOT IN ('completed','failed','aborted','lost')),
		(SELECT count(*) FROM bot_sessions WHERE bot_id=$1 AND (COALESCE(runtime_metadata->>'external_execution_run','')<>'' OR COALESCE(runtime_metadata->>'external_termination_unconfirmed','')<>'')),
		(SELECT count(*) FROM tool_approval_requests WHERE session_id IN (SELECT id FROM bot_sessions WHERE bot_id=$1) AND status='pending') +
		(SELECT count(*) FROM user_input_requests WHERE session_id IN (SELECT id FROM bot_sessions WHERE bot_id=$1) AND status='pending')`, fixture.botID).Scan(&active, &unresolved, &pending); err != nil || active != 0 || unresolved != 0 || pending != 0 {
		t.Fatalf("capacity fixture retained unresolved durable work: active=%d unresolved=%d pending=%d err=%v", active, unresolved, pending, err)
	}
}
