//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

func TestSREXT003UnconfirmedExternalExitResumesAfterLeftoversStop(t *testing.T) {
	fixture := requireFixture(t, false)
	prepareFakeModel(t)
	probe := requireLedger(t)
	marker := uniqueMarker("external-unknown")
	var settings, response map[string]any
	if err := fixture.api.request(http.MethodGet, "/bots/"+fixture.botID+"/settings", nil, &settings, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	modelID := stringValue(settings["chat_model_id"])
	var model map[string]any
	if err := fixture.api.request(http.MethodGet, "/models/"+modelID, nil, &model, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	model["config"] = map[string]any{"compatibilities": []string{"tool-call"}, "context_window": 262144, "thinking_mode": "none"}
	if err := fixture.api.request(http.MethodPut, "/models/"+modelID, model, &response, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	if err := fixture.api.request(http.MethodPost, "/bots", map[string]any{"name": marker, "display_name": marker, "wait_for_ready": true}, &response, http.StatusCreated); err != nil {
		t.Fatal(err)
	}
	fixture.botID = stringValue(response["id"])
	scriptPath, artifactPath := "/data/"+marker+".py", "/data/"+marker+".jsonl"
	var children, taskIDs []string
	t.Cleanup(func() {
		var artifact map[string]any
		if err := fixture.api.request(http.MethodGet, "/bots/"+fixture.botID+"/container/fs/read?path="+url.QueryEscape(artifactPath), nil, &artifact, http.StatusOK); err != nil {
			t.Logf("capture unknown artifact failed: %v", err)
		} else {
			t.Logf("unknown bot=%s children=%v tasks=%v artifact=%s receipts=%s", fixture.botID, children, taskIDs, artifactPath, stringValue(artifact["content"]))
		}
		if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/container/stop", nil, &response, http.StatusOK); err != nil {
			t.Errorf("stop unknown workspace: %v", err)
		}
		if err := fixture.api.request(http.MethodGet, "/bots/"+fixture.botID+"/container", nil, &response, http.StatusOK); err != nil || response["task_running"] != false || response["status"] != "stopped" {
			t.Errorf("unknown workspace was not physically stopped: %v, %v", response, err)
		}
	})
	if err := fixture.api.configureBot(fixture.botID, modelID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/container/fs/write", map[string]any{"path": scriptPath, "content": externalSubagentScript}, &response, http.StatusOK); err != nil {
		t.Fatal(err)
	}
	createBackend := func() string {
		t.Helper()
		if err := fixture.api.request(http.MethodPost, "/bots/"+fixture.botID+"/agents", map[string]any{
			"name": uniqueMarker("unknown-backend"), "runtime": "acp", "enabled": true,
			"metadata": map[string]any{"provider": "acp", "managed": map[string]any{"command": "/usr/bin/python3", "arguments": scriptPath + "\n" + artifactPath}},
		}, &response, http.StatusCreated); err != nil {
			t.Fatal(err)
		}
		return stringValue(response["id"])
	}
	backendID := createBackend()
	parentID := mustCreateSession(t, fixture, marker)
	invoke := func(phase, worker string) (any, bool) {
		t.Helper()
		turnMarker := uniqueMarker("unknown-" + phase)
		text := fmt.Sprintf("[acceptance:%s mode=external_subagent phase=%s backend=%s worker=%s task=none]", turnMarker, phase, backendID, worker)
		connection := mustDial(t, loadEnvironment().primaryURL, fixture)
		defer closeWebSocket(connection)
		mustSubscribeAndReadSnapshot(t, connection, parentID)
		_, admitted := mustSendAndAccept(t, fixture, connection, parentID, "invocation-"+turnMarker, text)
		_, terminal := mustReadRunTerminal(t, connection, admitted.RunID)
		if eventState(terminal) != "completed" {
			t.Fatalf("unknown parent failed: %#v", terminal)
		}
		rows, err := probe.runHistory(t.Context(), admitted.RunID)
		if err != nil {
			t.Fatal(err)
		}
		name := "spawn_agent"
		if phase == "resume" {
			name = "send_message"
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
						return toolexec.OutputValue(part.Result), part.IsError
					}
				}
			}
		}
		t.Fatalf("no persisted %s result", name)
		return nil, false
	}
	env := loadEnvironment()
	workspace := func(args ...string) ([]byte, error) {
		return exec.CommandContext(t.Context(), "docker", append([]string{"exec", env.primaryContainer, "ctr", "-n", "default", "task"}, args...)...).CombinedOutput() //nolint:gosec // explicit isolated acceptance container
	}
	t.Cleanup(func() { _, _ = workspace("resume", "workspace-"+fixture.botID) })
	for i := range 3 {
		output, failed := invoke("spawn_hold_escape", fmt.Sprintf("worker%d", i))
		result, ok := output.(map[string]any)
		if failed || !ok || result["status"] != "background_started" {
			t.Fatalf("unknown hold did not start: %v", output)
		}
		children = append(children, stringValue(result["session_id"]))
		taskIDs = append(taskIDs, stringValue(result["task_id"]))
		mustWaitRunState(t, children[i], "subagent:"+taskIDs[i], func(run sessionRunRecord) bool { return run.State == "running" })
	}
	var receipts []map[string]any
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		receipts = readExternalArtifact(t, fixture, artifactPath)
		if externalArtifactCount(receipts, "partial_sent") >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	pids, sessions := map[any]bool{}, map[any]bool{}
	escaped := map[string]string{}
	for _, row := range receipts {
		if row["event"] == "prompt" && row["action"] == "hold_escape" {
			pids[row["pid"]], sessions[row["session_id"]] = true, true
		}
		if row["event"] == "escaped" {
			escaped[stringValue(row["thread"])] = fmt.Sprint(row["escaped_pid"])
		}
	}
	if len(pids) != 3 || len(sessions) != 3 || externalArtifactCount(receipts, "prompt") != 3 || externalArtifactCount(receipts, "partial_sent") != 3 {
		t.Fatalf("unknown scenario lacks three real dispatched holds: %v", receipts)
	}
	var running int
	if err := probe.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_runs WHERE session_id=ANY($1::uuid[]) AND state='running'`, children).Scan(&running); err != nil || running != 3 {
		t.Fatalf("three dispatched holds are not simultaneously durable running: count=%d err=%v", running, err)
	}
	hasPartial := func(runID string) bool {
		t.Helper()
		history, err := probe.runHistory(t.Context(), runID)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range history {
			if strings.Contains(string(row.Content), "external-subagent-partial-hold_escape") {
				return true
			}
		}
		return false
	}
	for _, childID := range children {
		if escaped[childID] == "" {
			t.Fatalf("child %s process does not carry its thread marker: %v", childID, receipts)
		}
	}
	marked := func(pid, childID string) bool {
		script := fmt.Sprintf("tr '\\0' '\\n' </proc/%s/environ | grep -qx 'MEMOH_SUBAGENT_THREAD=%s'", pid, childID)
		_, err := workspace("exec", "--exec-id", uniqueMarker("probe"), "workspace-"+fixture.botID, "sh", "-c", script)
		return err == nil
	}
	if output, err := workspace("pause", "workspace-"+fixture.botID); err != nil {
		t.Fatalf("freeze workspace: %v: %s", err, output)
	}
	connection := mustDial(t, env.primaryURL, fixture)
	for i, childID := range children {
		run := mustWaitRunState(t, childID, "subagent:"+taskIDs[i], func(run sessionRunRecord) bool { return run.State == "running" })
		if err := sendAbort(connection, childID, run.RunID, "control-"+uniqueMarker("unknown")); err != nil {
			t.Fatal(err)
		}
	}
	closeWebSocket(connection)
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancelWait()
	unknownRuns := map[string]string{}
	for i, childID := range children {
		run, err := probe.waitRun(waitCtx, childID, "subagent:"+taskIDs[i], func(run sessionRunRecord) bool { return run.terminal() })
		if err != nil || run.State != "aborted" {
			t.Fatalf("frozen workspace did not leave an unconfirmed execution: %#v %v", run, err)
		}
		var execution, unknown string
		if err := probe.pool.QueryRow(t.Context(), `SELECT runtime_metadata->>'external_execution_run',runtime_metadata->>'external_termination_unconfirmed' FROM bot_sessions WHERE id=$1`, childID).Scan(&execution, &unknown); err != nil || execution != run.RunID || unknown != run.RunID {
			t.Fatalf("unconfirmed durable identity mismatch: %s %s %v", execution, unknown, err)
		}
		if !hasPartial(run.RunID) {
			t.Fatalf("unknown run lost partial transcript: %s", run.RunID)
		}
		assertTerminalHistory(t, run)
		unknownRuns[childID] = run.RunID
		t.Logf("unconfirmed child=%s run=%s task=%s escaped_pid=%s", childID, run.RunID, taskIDs[i], escaped[childID])
	}
	if output, err := workspace("resume", "workspace-"+fixture.botID); err != nil {
		t.Fatalf("thaw workspace: %v: %s", err, output)
	}
	for deadline := time.Now().Add(time.Minute); fixture.api.request(http.MethodGet, "/bots/"+fixture.botID+"/container/fs/read?path="+url.QueryEscape(artifactPath), nil, &response, http.StatusOK) != nil; {
		if time.Now().After(deadline) {
			t.Fatal("thawed workspace did not become reachable")
		}
		time.Sleep(time.Second)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		output, err := workspace("exec", "--exec-id", uniqueMarker("probe"), "workspace-"+fixture.botID, "sh", "-c", "grep -l 'MEMOH_SUBAGENT_THREA[D]=' /proc/[0-9]*/cmdline 2>/dev/null; true")
		if err != nil {
			t.Fatalf("probe reapers: %v: %s", err, output)
		}
		if len(bytes.TrimSpace(output)) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reapers sent before the thaw did not finish: %s", output)
		}
	}
	planted := map[string]string{}
	for _, childID := range children {
		var execution, unknown string
		if err := probe.pool.QueryRow(t.Context(), `SELECT runtime_metadata->>'external_execution_run',runtime_metadata->>'external_termination_unconfirmed' FROM bot_sessions WHERE id=$1`, childID).Scan(&execution, &unknown); err != nil || execution != unknownRuns[childID] || unknown != unknownRuns[childID] {
			t.Fatalf("thawing released %s before its leftovers were stopped: %s %s %v", childID, execution, unknown, err)
		}
		output, err := workspace("exec", "--exec-id", uniqueMarker("plant"), "workspace-"+fixture.botID, "sh", "-c", fmt.Sprintf("env MEMOH_SUBAGENT_THREAD=%s sleep 600 </dev/null >/dev/null 2>&1 & echo $!", childID))
		if err != nil {
			t.Fatalf("plant leftover for %s: %v: %s", childID, err, output)
		}
		planted[childID] = strings.TrimSpace(string(output))
	}
	time.Sleep(time.Second)
	for _, childID := range children {
		if !marked(planted[childID], childID) {
			t.Fatalf("no leftover process for %s: the scenario cannot show reaping", childID)
		}
	}
	output, failed := invoke("capacity_spawn", "fourth")
	if result, ok := output.(map[string]any); failed || !ok || result["status"] != "completed" {
		t.Fatalf("reaped executions still hold capacity: %v", output)
	}
	for _, childID := range children {
		if marked(escaped[childID], childID) || marked(planted[childID], childID) {
			t.Fatalf("leftover process of %s survived termination confirmation", childID)
		}
		var execution, unknown, resolved *string
		if err := probe.pool.QueryRow(t.Context(), `SELECT runtime_metadata->>'external_execution_run',runtime_metadata->>'external_termination_unconfirmed',runtime_metadata->>'external_termination_resolved' FROM bot_sessions WHERE id=$1`, childID).Scan(&execution, &unknown, &resolved); err != nil || execution != nil || unknown != nil || resolved == nil || *resolved != unknownRuns[childID] {
			t.Fatalf("child %s was not settled: %v %v %v %v", childID, execution, unknown, resolved, err)
		}
	}
	output, failed = invoke("resume", "worker0")
	if result, ok := output.(map[string]any); failed || !ok || result["status"] != "completed" {
		t.Fatalf("settled child did not run again: %v", output)
	}
	receipts = readExternalArtifact(t, fixture, artifactPath)
	notices := map[string]int{}
	for _, row := range receipts {
		if row["event"] == "prompt" {
			notices[stringValue(row["action"])] += map[bool]int{true: 1}[row["notice"] == true]
		}
	}
	if externalArtifactCount(receipts, "prompt") != 5 || notices["resume"] != 1 || notices["initial"]+notices["hold_escape"] != 0 || externalArtifactCount(receipts, "unexpected_effect") != 0 {
		t.Fatalf("settled children were replayed or the resumed child was not warned: %v", receipts)
	}
	var resolved *string
	if err := probe.pool.QueryRow(t.Context(), `SELECT runtime_metadata->>'external_termination_resolved' FROM bot_sessions WHERE id=$1`, children[0]).Scan(&resolved); err != nil || resolved != nil {
		t.Fatalf("notice was not consumed by the next run: %v %v", resolved, err)
	}
}
