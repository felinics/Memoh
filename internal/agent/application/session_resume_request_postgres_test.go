package application

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type readImageTools struct{}

func (readImageTools) Tools(context.Context, tools.SessionContext) ([]toolexec.Tool, error) {
	type readArgs struct {
		Path string `json:"path"`
	}
	return []toolexec.Tool{toolexec.Define(tools.ReadMediaToolName().String(), "Read a workspace file.",
		func(_ *toolexec.ToolExecContext, args readArgs) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue(tools.ReadMediaToolOutput{
				Public:         tools.ReadMediaToolResult{OK: true, Path: args.Path, Mime: "image/png"},
				ImageBase64:    base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n\x00screenshot")),
				ImageMediaType: "image/png",
			}), nil
		})}, nil
}

type resumeUnavailableWorkspace struct{ calls int }

func (w *resumeUnavailableWorkspace) ResolveWorkspaceTarget(context.Context, string, string) (workspace.ResolvedWorkspaceTarget, error) {
	w.calls++
	// Both initial target selection and the later run-config lookup can fail
	// after the readiness check. No model/tool has executed at either boundary.
	if w.calls == 1 || w.calls == 3 {
		return workspace.ResolvedWorkspaceTarget{}, fmt.Errorf("restart window: %w", bridge.ErrUnavailable)
	}
	return workspace.ResolvedWorkspaceTarget{TargetID: "native", Kind: "native"}, nil
}

func TestPostgresShutdownResumeReadsImageAfterWorkspaceRecovery(t *testing.T) {
	h := newWSStepHistoryHarness(t, wsStepHistorySuccess)
	runID := h.interruptedTurn(t, h.startProcess(t), "inspect the desktop")
	next := h.startProcess(t)
	next.service.streamHTTPClient.Transport.(*wsStepHistoryTransport).mode = wsStepHistoryReadImage
	config := next.service.queries.(resumeLedgerQueries).directLifecycleQueries
	model := config.models["direct-lifecycle-model"]
	model.Config = []byte(`{"context_window":128000,"compatibilities":["tool-call","vision"]}`)
	config.models["direct-lifecycle-model"] = model
	next.service.agent.SetToolProviders([]tools.ToolProvider{readImageTools{}})
	target := &resumeUnavailableWorkspace{}
	next.service.SetWorkspaceTargetResolver(target)
	next.service.SetResumeReadiness(func(context.Context, string, string) error { return nil })
	if !h.resumeScan(t, next, runID) {
		t.Fatal("continuation was not admitted")
	}
	var resumed, state, originalTurn string
	err := h.pool.QueryRow(t.Context(), `SELECT run_id::text,state FROM session_runs WHERE invocation_id=$1`, "resume:"+runID).Scan(&resumed, &state)
	if err != nil || state != "completed" {
		t.Fatalf("resume: state=%s err=%v", state, err)
	}
	if err := h.pool.QueryRow(t.Context(), `SELECT turn_id::text FROM session_runs WHERE run_id=$1`, runID).Scan(&originalTurn); err != nil {
		t.Fatal(err)
	}
	var feedback, foreign, requests int
	err = h.pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE metadata->>'message_source'='internal_feedback'),count(*) FILTER(WHERE turn_id<>$2::uuid),count(*) FILTER(WHERE role='user' AND COALESCE(metadata->>'message_source','')<>'internal_feedback') FROM bot_history_messages WHERE run_id=$1`, resumed, originalTurn).Scan(&feedback, &foreign, &requests)
	if err != nil || feedback != 1 || foreign != 0 || requests != 0 {
		t.Fatalf("history: feedback=%d foreign=%d requests=%d err=%v", feedback, foreign, requests, err)
	}
	snapshot, err := next.manager.Snapshot(t.Context(), h.botID, h.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	view := snapshot.CurrentRunView
	if view == nil || view.ResumeTurn == nil || view.ResumeTurn.TurnID != originalTurn || view.TurnID == originalTurn || len(view.ResumeTurn.Messages) == 0 {
		t.Fatalf("resume projection: %+v", view)
	}
	// A subsequent shutdown and a decision continuation use this run's intent,
	// even though it wrote output but no request of its own.
	row, err := next.service.queries.GetSessionRun(t.Context(), db.ParseUUIDOrEmpty(resumed))
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := decodeRunAdmission(row.InputJson)
	if err != nil || admitted.Resume == nil || admitted.Resume.RequestMessageID == "" {
		t.Fatalf("saved request: %+v, %v", admitted.Resume, err)
	}
	request, err := next.service.interruptedRunRequest(t.Context(), row, admitted.Resume)
	if err != nil || request.restore || request.messageID != admitted.Resume.RequestMessageID {
		t.Fatalf("next shutdown request: %+v, %v", request, err)
	}
	decisionRequest, err := next.service.continuationTurnRequestMessageID(t.Context(), h.sessionID, sessionruntime.RunHandle{RunID: resumed, TurnID: row.TurnID.String()})
	if err != nil || decisionRequest != request.messageID {
		t.Fatalf("decision request: %q, %v", decisionRequest, err)
	}

	if target.calls < 4 {
		t.Fatalf("setup was not retried: %d calls", target.calls)
	}
}
