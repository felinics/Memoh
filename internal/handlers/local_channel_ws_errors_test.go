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

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/application"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/errs"
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
// the generic permission_mode_failed code; the cause is recorded, since the
// answer does not carry it.
func TestPermissionQuickActionRecordsAnUnclassifiedCause(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	agentService := application.NewService(slog.New(slog.DiscardHandler), nil, nil, nil, nil, nil, nil, time.UTC, time.Second)
	agentService.SetSessionService(failingSessionService{err: errors.New("SECRET session store unreachable")})
	agentService.SetBotPermissionChecker(allowAllPermissions{})
	handler := &LocalChannelHandler{agentService: agentService, logger: slog.New(slog.NewJSONHandler(&logs, nil))}

	_, slashErr := handler.executeWebPermissionQuickAction(context.Background(), "bot-1", webQuickActionContext{SessionID: "session-1", ActorID: "user-1"})
	if slashErr == nil || slashErr.Code != slash.CodePermissionModeFailed {
		t.Fatalf("slash error = %+v, want %s", slashErr, slash.CodePermissionModeFailed)
	}
	if !strings.Contains(logs.String(), `"msg":"permission mode change failed"`) || !strings.Contains(logs.String(), "SECRET session store unreachable") {
		t.Fatalf("records = %s, want the cause recorded", logs.String())
	}
}
