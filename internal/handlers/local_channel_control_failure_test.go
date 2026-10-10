package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/accounts"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// controlFailureRuntime answers every control with the configured outcome.
type controlFailureRuntime struct {
	testWSDecisionRuntime
	abortApplied bool
	abortErr     error
	routeErr     error
}

func (r *controlFailureRuntime) AbortControl(context.Context, string, string, string, string) (bool, error) {
	return r.abortApplied, r.abortErr
}

func (r *controlFailureRuntime) RouteDecisionResponse(context.Context, sessionruntime.DecisionResponse) (sessionruntime.DecisionResponseResult, error) {
	return sessionruntime.DecisionResponseResult{Handled: true}, r.routeErr
}

func controlFailureHandler(logs *lockedBuffer, runtime *controlFailureRuntime, sessionID string) (*LocalChannelHandler, string, string) {
	const (
		botID = "11111111-1111-1111-1111-111111111111"
		user  = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	queries := localChannelSessionAuthQueries{
		bot: testBotRow(botID, map[string]any{}),
		session: sqlc.BotSession{
			ID:              testUUID(sessionID),
			BotID:           testUUID(botID),
			Type:            sessionpkg.TypeChat,
			SessionMode:     sessionpkg.TypeChat,
			CreatedByUserID: testUUID(user),
			Metadata:        []byte(`{}`),
		},
		grants: []sqlc.ListBotUserGrantsForUserRow{{
			ID:          testUUID("dddddddd-dddd-dddd-dddd-dddddddddddd"),
			BotID:       testUUID(botID),
			SubjectType: bots.GrantSubjectUser,
			UserID:      testUUID(user),
			Permissions: []byte(`["chat","workspace_exec"]`),
		}},
	}
	handler, _, _ := heartbeatTestHandler(logs)
	handler.botService = bots.NewService(nil, queries)
	handler.accountService = accounts.NewService(nil, testAdminAccountStore{role: "user"})
	handler.sessionService = sessionpkg.NewService(nil, queries, nil)
	handler.sessionRuntime = runtime
	handler.channelType = channel.ChannelTypeLocal
	return handler, botID, user
}

func wsRequestRecords(logs *lockedBuffer) []map[string]any {
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "ws request" {
			records = append(records, record)
		}
	}
	return records
}

// A control that fails answers with its control_ack and ends with exactly one
// ws request record naming the control and the cause. A control that resolved
// without changing anything is not a failure and writes none.
func TestWSControlFailuresWriteOneResultRecord(t *testing.T) {
	t.Parallel()
	const (
		sessionID = "22222222-2222-2222-2222-222222222222"
		runID     = "33333333-3333-3333-3333-333333333333"
		controlID = "control-1"
	)
	tests := []struct {
		name       string
		msg        map[string]any
		runtime    *controlFailureRuntime
		operation  string
		wantCode   string
		wantFault  string
		wantRecord bool
	}{
		{
			name:       "tool approval no longer pending",
			msg:        map[string]any{"type": "tool_approval_response", "decision_id": "d1", "decision": "approve"},
			runtime:    &controlFailureRuntime{routeErr: toolapproval.ErrNotFound},
			operation:  "ws.control.tool_approval",
			wantCode:   string(apperror.CodeToolApprovalNotFound),
			wantFault:  "client",
			wantRecord: true,
		},
		{
			name:       "user input routing failure",
			msg:        map[string]any{"type": "user_input_response", "decision_id": "d1"},
			runtime:    &controlFailureRuntime{routeErr: errors.New("store down")},
			operation:  "ws.control.user_input",
			wantCode:   string(apperror.CodeUserInputOperationFailed),
			wantFault:  "server",
			wantRecord: true,
		},
		{
			name:       "user input forbidden",
			msg:        map[string]any{"type": "user_input_response", "decision_id": "d1"},
			runtime:    &controlFailureRuntime{routeErr: userinput.ErrForbidden},
			operation:  "ws.control.user_input",
			wantCode:   string(apperror.CodeUserInputForbidden),
			wantFault:  "client",
			wantRecord: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs lockedBuffer
			handler, botID, user := controlFailureHandler(&logs, tt.runtime, sessionID)
			client := openLocalChannelTestWS(t, handler, botID, user)
			tt.msg["session_id"] = sessionID
			tt.msg["run_id"] = runID
			tt.msg["control_id"] = controlID
			if err := client.WriteJSON(tt.msg); err != nil {
				t.Fatal(err)
			}
			var ack wsOutboundEvent
			if err := client.ReadJSON(&ack); err != nil {
				t.Fatal(err)
			}
			if ack.Type != "control_ack" || ack.ControlID != controlID || ack.RunID != runID || ack.Applied || ack.Code != tt.wantCode {
				t.Fatalf("ack = %#v, want an unapplied control_ack with code %q", ack, tt.wantCode)
			}
			// A later request on the same socket is answered after the control
			// has been handled, so by then the record exists or never will.
			if err := client.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
				t.Fatal(err)
			}
			var barrier map[string]any
			if err := client.ReadJSON(&barrier); err != nil {
				t.Fatal(err)
			}
			var control []map[string]any
			for _, r := range wsRequestRecords(&logs) {
				if op, _ := r["operation"].(string); strings.HasPrefix(op, "ws.control.") {
					control = append(control, r)
				}
			}
			if !tt.wantRecord {
				if len(control) != 0 {
					t.Fatalf("records = %v, want none for a resolved control", control)
				}
				return
			}
			if len(control) != 1 {
				t.Fatalf("records = %v, want exactly one", control)
			}
			r := control[0]
			if r["operation"] != tt.operation || r["fault"] != tt.wantFault || r["session_id"] != sessionID || r["reason"] == nil {
				t.Fatalf("record = %v, want %s with fault %s and a reason", r, tt.operation, tt.wantFault)
			}
		})
	}
}

// A session the caller may not address fails the control as session.not_found,
// by code, whichever control carried it.
func TestWSControlOnAnUnknownSessionRecordsSessionNotFound(t *testing.T) {
	t.Parallel()
	var logs lockedBuffer
	handler, botID, user := controlFailureHandler(&logs, &controlFailureRuntime{}, "22222222-2222-2222-2222-222222222222")
	handler.sessionService = sessionpkg.NewService(nil, localChannelSessionAuthQueries{
		getSessionByID: func(context.Context, pgtype.UUID) (sqlc.BotSession, error) {
			return sqlc.BotSession{}, pgx.ErrNoRows
		},
	}, nil)
	client := openLocalChannelTestWS(t, handler, botID, user)
	if err := client.WriteJSON(map[string]any{
		"type":        "tool_approval_response",
		"session_id":  "99999999-9999-9999-9999-999999999999",
		"run_id":      "run-1",
		"decision_id": "d1",
		"control_id":  "c1",
	}); err != nil {
		t.Fatal(err)
	}
	var ack wsOutboundEvent
	if err := client.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}
	if ack.Type != "control_ack" || ack.Applied || ack.Code != string(apperror.CodeToolApprovalForbidden) {
		t.Fatalf("ack = %#v, want the forbidden ack unchanged", ack)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		records := wsRequestRecords(&logs)
		if len(records) == 1 {
			r := records[0]
			if r["operation"] != "ws.control.tool_approval" || r["reason"] != string(apperror.CodeSessionNotFound) || r["fault"] != "client" {
				t.Fatalf("record = %v, want a client session.not_found record", r)
			}
			return
		}
		if len(records) > 1 || time.Now().After(deadline) {
			t.Fatalf("records = %v, want exactly one", records)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An abort the session runtime could not route answers with its ack and ends
// with one abort record. An abort that resolved without changing anything, the
// run having already finished, is not a failure and writes none.
func TestAbortWSRunFailureWritesOneResultRecord(t *testing.T) {
	t.Parallel()
	const (
		sessionID = "session-1"
		runID     = "run-1"
	)
	msg := wsClientMessage{SessionID: sessionID, ControlID: "control-1"}

	var logs bytes.Buffer
	handler := &LocalChannelHandler{
		logger:         slog.New(slog.NewJSONHandler(&logs, nil)),
		sessionRuntime: &controlFailureRuntime{abortErr: errors.New("owner unreachable")},
	}
	frame := decodeWSTestEvent(t, func(writer *wsWriter) {
		handler.abortWSRun(context.Background(), writer, "bot-1", msg, runID)
	})
	if frame["type"] != "control_ack" || frame["control_id"] != "control-1" || frame["applied"] == true {
		t.Fatalf("frame = %#v, want an unapplied control_ack", frame)
	}
	if _, present := frame["code"]; present {
		t.Fatalf("frame = %#v, want the ack unchanged: no code for an uncoded cause", frame)
	}
	record := onlyRecord(t, &logs)
	if record["msg"] != "ws request" || record["operation"] != "ws.control.abort" || record["fault"] != "server" || record["session_id"] != sessionID {
		t.Fatalf("record = %v, want a server ws.control.abort record", record)
	}

	logs.Reset()
	handler.sessionRuntime = &controlFailureRuntime{}
	frame = decodeWSTestEvent(t, func(writer *wsWriter) {
		handler.abortWSRun(context.Background(), writer, "bot-1", msg, runID)
	})
	if frame["type"] != "control_ack" {
		t.Fatalf("frame = %#v, want a control_ack", frame)
	}
	if logs.Len() != 0 {
		t.Fatalf("records = %s, want none for a resolved abort", logs.String())
	}
}
