package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/application"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/errs"
	skillset "github.com/felinics/memoh/internal/skills"
	"github.com/felinics/memoh/internal/slash"
)

// A WebSocket request that fails before a run takes it over is answered with a
// frame carrying a code and its catalog detail, and ends with one result
// record carrying the cause. The cause's text is in the record and not in the
// frame.
func TestFailWSRequestAnswersByCodeAndRecordsTheCause(t *testing.T) {
	t.Parallel()
	const secret = "SECRET dial unix /run/memoh.sock"
	tests := []struct {
		name        string
		err         error
		wantType    string
		wantCode    string
		wantLevel   string
		wantFault   string
		wantMessage string
	}{
		{
			name:        "uncoded failure",
			err:         errors.New(secret),
			wantType:    "error",
			wantCode:    "internal",
			wantLevel:   "ERROR",
			wantFault:   "server",
			wantMessage: "Something went wrong on the server. Please try again.",
		},
		{
			name:        "refused request",
			err:         echo.NewHTTPError(http.StatusNotFound, "session not found").WithInternal(errors.New(secret)),
			wantType:    "error",
			wantCode:    "http.not_found",
			wantLevel:   "INFO",
			wantFault:   "client",
			wantMessage: "The requested resource was not found.",
		},
		{
			name:        "catalogued failure",
			err:         apperror.Wrap(apperror.CodeWorkspaceUnreachable, errors.New(secret), nil),
			wantType:    "error",
			wantCode:    "workspace.unreachable",
			wantLevel:   "ERROR",
			wantFault:   "server",
			wantMessage: "The workspace could not be reached.",
		},
		{
			name:        "busy session",
			err:         sessionruntime.ErrSessionBusy,
			wantType:    "run_rejected",
			wantCode:    "session_runtime.session_busy",
			wantLevel:   "INFO",
			wantFault:   "client",
			wantMessage: "This conversation is still working on the previous message. Please try again shortly.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			frame := decodeWSTestEvent(t, func(writer *wsWriter) {
				failWSRequest(context.Background(), logger, writer, "bot-1", wsTurn("invocation-1", "session-1"), "ws.message", tt.err)
			})
			if frame["type"] != tt.wantType || frame["code"] != tt.wantCode || frame["message"] != tt.wantMessage {
				t.Fatalf("frame = %#v, want %s %s %q", frame, tt.wantType, tt.wantCode, tt.wantMessage)
			}
			encoded, _ := json.Marshal(frame)
			if strings.Contains(string(encoded), "SECRET") {
				t.Fatalf("frame carries the cause: %s", encoded)
			}

			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("records = %q, want one result record", lines)
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
				t.Fatal(err)
			}
			if record["msg"] != "ws request" || record["level"] != tt.wantLevel || record["fault"] != tt.wantFault ||
				record["operation"] != "ws.message" || record["bot_id"] != "bot-1" || record["session_id"] != "session-1" ||
				record["invocation_id"] != "invocation-1" {
				t.Fatalf("record = %v, want a %s ws request record with fault %s", record, tt.wantLevel, tt.wantFault)
			}
			if tt.name != "busy session" && !strings.Contains(record["error"].(string), "SECRET") {
				t.Fatalf("record error = %v, want the cause", record["error"])
			}
		})
	}
}

func TestFailWSRequestClientValidationRecordsLiteral(t *testing.T) {
	t.Parallel()

	const cause = "invocation_id is required"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	frame := decodeWSTestEvent(t, func(writer *wsWriter) {
		failWSRequest(context.Background(), logger, writer, "bot-1", wsTurn("", "session-1"), "ws.message", echo.NewHTTPError(http.StatusBadRequest, cause))
	})
	if frame["type"] != "error" || frame["code"] != "http.bad_request" || frame["message"] != "The request is invalid." {
		t.Fatalf("frame = %#v, want http.bad_request with catalog detail", frame)
	}
	encoded, _ := json.Marshal(frame)
	if strings.Contains(string(encoded), cause) {
		t.Fatalf("frame leaked validation cause %q: %s", cause, encoded)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(logs.String())), &record); err != nil {
		t.Fatal(err)
	}
	if record["msg"] != "ws request" || record["level"] != "INFO" || record["fault"] != "client" || record["operation"] != "ws.message" {
		t.Fatalf("record = %#v, want INFO client ws.message result", record)
	}
	if got, ok := record["error"].(string); !ok || !strings.Contains(got, cause) {
		t.Fatalf("record error = %#v, want %q", record["error"], cause)
	}
}

func TestFailWSRequestMissingServiceRecordsServerFault(t *testing.T) {
	t.Parallel()

	const cause = "session runtime is not configured"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	frame := decodeWSTestEvent(t, func(writer *wsWriter) {
		failWSRequest(context.Background(), logger, writer, "bot-1", wsTurn("invocation-1", "session-1"), "ws.message", errs.New(cause))
	})
	if frame["type"] != "error" || frame["code"] != "internal" || frame["message"] != "Something went wrong on the server. Please try again." {
		t.Fatalf("frame = %#v, want internal with catalog detail", frame)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(logs.String())), &record); err != nil {
		t.Fatal(err)
	}
	if record["msg"] != "ws request" || record["level"] != "ERROR" || record["fault"] != "server" || record["operation"] != "ws.message" {
		t.Fatalf("record = %#v, want ERROR server ws.message result", record)
	}
	if got, ok := record["error"].(string); !ok || !strings.Contains(got, cause) {
		t.Fatalf("record error = %#v, want %q", record["error"], cause)
	}
}

// A runner error that is how the run ended is sent under the code the run was
// recorded with. An error returned after the run's failure was delivered is
// not the run's outcome and is answered as a request error.
func TestSendWSRunFailureNamesTheRecordedCode(t *testing.T) {
	t.Parallel()
	ref := wsTurn("invocation-1", "session-1").withRun("run-1")
	cause := errors.New("SECRET resolve failed")

	frame := decodeWSTestEvent(t, func(writer *wsWriter) {
		sendWSRunFailure(context.Background(), writer, ref, cause, wsRunOutcome(application.RunOutcome{}, cause), apperror.CodeRuntimeRunFailed)
	})
	if frame["code"] != "runtime_run_failed" || frame["message"] != "The response could not be completed. Please try again." {
		t.Fatalf("frame = %#v, want the recorded code", frame)
	}

	later := errors.New("SECRET replace history turn")
	frame = decodeWSTestEvent(t, func(writer *wsWriter) {
		sendWSRunFailure(context.Background(), writer, ref, later, wsRunOutcome(application.RunOutcome{}, cause), apperror.CodeAgentProviderOverloaded)
	})
	if frame["code"] != "internal" {
		t.Fatalf("frame = %#v, want internal for an error that is not the run's outcome", frame)
	}
}

type failingSessionService struct{ err error }

func (f failingSessionService) Get(context.Context, string) (session.Thread, error) {
	return session.Thread{}, f.err
}

func (f failingSessionService) UpdateTitle(context.Context, string, string) (session.Thread, error) {
	return session.Thread{}, f.err
}

func (f failingSessionService) UpdateMetadata(context.Context, string, map[string]any) (session.Thread, error) {
	return session.Thread{}, f.err
}

func (f failingSessionService) MergeRuntimeMetadata(context.Context, string, string, map[string]any) (session.Thread, error) {
	return session.Thread{}, f.err
}

type allowAllPermissions struct{}

func (allowAllPermissions) HasBotPermission(context.Context, string, string, string) (bool, error) {
	return true, nil
}

// A permission action that fails for an unclassified reason is answered with
// runtime_control.failed. The action records nothing itself: the command_error
// frame carries only the code, and the request's one result record carries
// the cause.
func TestPermissionQuickActionFailureIsRecordedOnceByTheRequest(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	agentService := application.NewService(slog.New(slog.DiscardHandler), nil, nil, nil, nil, nil, nil, time.UTC, time.Second)
	agentService.SetSessionService(failingSessionService{err: errors.New("SECRET session store unreachable")})
	agentService.SetBotPermissionChecker(allowAllPermissions{})
	handler := &LocalChannelHandler{agentService: agentService, logger: logger}

	_, err := handler.executeWebPermissionQuickAction(context.Background(), "bot-1", webQuickActionContext{SessionID: "session-1", ActorID: "user-1"})
	if apperror.CodeOf(err) != apperror.CodeRuntimeControlFailed {
		t.Fatalf("error = %v, want %s", err, apperror.CodeRuntimeControlFailed)
	}
	if logs.Len() != 0 {
		t.Fatalf("records = %s, want none from the action", logs.String())
	}
	assertWSCommandFailure(t, handler, &logs, err, string(apperror.CodeRuntimeControlFailed), "SECRET session store unreachable")
}

// A deprecated message_id that does not resolve is refused as not found. The
// lookup error is the 404's internal error, so the request's one result record
// carries it and the frame does not.
func TestResolveWSTargetTurnIDFailureReachesTheResultRecord(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := &LocalChannelHandler{logger: logger, agentService: &application.Service{}}

	_, err := handler.resolveWSTargetTurnID(context.Background(), "session-1", "", "message-1")
	if err == nil {
		t.Fatal("resolveWSTargetTurnID succeeded, want not found")
	}
	frame := decodeWSTestEvent(t, func(writer *wsWriter) {
		failWSRequest(context.Background(), logger, writer, "bot-1", wsTurn("invocation-1", "session-1"), "ws.retry_message", err)
	})
	if frame["code"] != string(apperror.CodeSessionTurnNotFound) {
		t.Fatalf("frame = %#v, want session_runtime.turn_not_found", frame)
	}
	if encoded, _ := json.Marshal(frame); strings.Contains(string(encoded), "message service") {
		t.Fatalf("frame carries the cause: %s", encoded)
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("records = %q, want one result record", lines)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record["msg"] != "ws request" || record["fault"] != "client" {
		t.Fatalf("record = %v, want a client ws request record", record)
	}
	if got, _ := record["error"].(string); !strings.Contains(got, "message service not configured") {
		t.Fatalf("record error = %#v, want the lookup cause", record["error"])
	}
}

type failingSkillResolver struct{ testRuntimeSkillResolver }

func (failingSkillResolver) ListSafeSkillCatalog(context.Context, string) ([]skillset.SafeCatalogItem, error) {
	return nil, errors.New("SECRET skill store unreachable")
}

// A skill catalog that cannot be read is this process's failure, answered as
// internal; the frame carries no text of the cause and the request's one
// result record carries it.
func TestSkillListFailureIsAnsweredAsInternalAndRecordedOnce(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := &LocalChannelHandler{logger: logger, skillResolver: failingSkillResolver{}}

	result, err := handler.executeWebQuickAction(context.Background(), "bot-1", "skill.list", true, webQuickActionContext{})
	if result != nil || err == nil || apperror.CodeOf(err) != "" {
		t.Fatalf("executeWebQuickAction = %v, %v, want the catalog failure", result, err)
	}
	if logs.Len() != 0 {
		t.Fatalf("records = %s, want none from the action", logs.String())
	}
	assertWSCommandFailure(t, handler, &logs, err, string(apperror.CodeInternal), "SECRET skill store unreachable")
}

// A slash refusal is the client's: the frame names its code and the request's
// one result record is a client record.
func TestSlashRefusalFrameAndRecord(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := &LocalChannelHandler{logger: logger}
	frame := decodeWSTestEvent(t, func(writer *wsWriter) {
		handler.failWSCommand(context.Background(), writer, "bot-1", wsClientMessage{InvocationID: "invocation-1"}, slash.NewError(slash.CodeRequestedSkillNotFound))
	})
	if frame["type"] != "command_error" || frame["code"] != string(apperror.CodeSlashSkillNotFound) || frame["fault"] != "client" {
		t.Fatalf("frame = %#v, want the skill_not_found refusal", frame)
	}
	record := onlyRecord(t, &logs)
	if record["msg"] != "ws request" || record["fault"] != "client" || record["reason"] != string(apperror.CodeSlashSkillNotFound) {
		t.Fatalf("record = %v, want a client ws request record", record)
	}
}

// A frame that is not JSON is answered with http.bad_request and ends with one
// client result record.
func TestUnparsableWSFrameIsAnsweredByCode(t *testing.T) {
	t.Parallel()
	var logs lockedBuffer
	handler, botID, user := heartbeatTestHandler(&logs)
	client := openLocalChannelTestWS(t, handler, botID, user)
	if err := client.WriteMessage(websocket.TextMessage, []byte("SECRET not json")); err != nil {
		t.Fatal(err)
	}
	var frame map[string]any
	if err := client.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	if frame["type"] != "error" || frame["code"] != string(apperror.CodeHTTPBadRequest) || frame["fault"] != "client" {
		t.Fatalf("frame = %#v, want http.bad_request", frame)
	}
	if encoded, _ := json.Marshal(frame); strings.Contains(string(encoded), "SECRET") || strings.Contains(string(encoded), "invalid character") {
		t.Fatalf("frame carries the cause: %s", encoded)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var records []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var record map[string]any
			if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "ws request" {
				records = append(records, record)
			}
		}
		if len(records) == 1 && records[0]["fault"] == "client" && records[0]["reason"] == string(apperror.CodeHTTPBadRequest) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("records = %v, want one client ws request record", records)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertWSCommandFailure(t *testing.T, handler *LocalChannelHandler, logs *bytes.Buffer, err error, code, cause string) {
	t.Helper()
	frame := decodeWSTestEvent(t, func(writer *wsWriter) {
		handler.failWSCommand(context.Background(), writer, "bot-1", wsClientMessage{InvocationID: "invocation-1", SessionID: "session-1"}, err)
	})
	if frame["type"] != "command_error" || frame["code"] != code {
		t.Fatalf("frame = %#v, want code %s", frame, code)
	}
	if encoded, _ := json.Marshal(frame); strings.Contains(string(encoded), "SECRET") {
		t.Fatalf("frame carries the cause: %s", encoded)
	}
	record := onlyRecord(t, logs)
	if record["msg"] != "ws request" || record["level"] != "ERROR" {
		t.Fatalf("record = %v, want an ERROR ws request record", record)
	}
	if got, _ := record["error"].(string); !strings.Contains(got, cause) {
		t.Fatalf("record error = %#v, want the cause", record["error"])
	}
}

func onlyRecord(t *testing.T, logs *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("records = %q, want one result record", lines)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	return record
}
