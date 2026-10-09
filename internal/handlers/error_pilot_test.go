package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/botworkspace"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/server"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestCreateBotHTTPErrorMapsNameConflictToStableCode(t *testing.T) {
	err := createBotHTTPError(bots.ErrBotNameTaken, true)
	if got := apperror.CodeOf(err); got != apperror.CodeBotNameTaken {
		t.Fatalf("code = %q, want %q", got, apperror.CodeBotNameTaken)
	}
	if got := apperror.ArgsOf(err)["field"]; got != "name" {
		t.Fatalf("field arg = %q, want name", got)
	}
}

func TestCreateBotHTTPErrorMapsWorkspaceBootstrapFailureToStableCode(t *testing.T) {
	cause := errors.Join(
		workspace.ErrWorkspaceTemplateBootstrapFailed,
		errors.New("write /data/AGENTS.md: permission denied"),
	)
	err := createBotHTTPError(cause, true)
	if got := apperror.CodeOf(err); got != apperror.CodeWorkspaceTemplateBootstrapFailed {
		t.Fatalf("code = %q, want %q", got, apperror.CodeWorkspaceTemplateBootstrapFailed)
	}
	if got := apperror.CauseOf(err); !errors.Is(got, workspace.ErrWorkspaceTemplateBootstrapFailed) {
		t.Fatalf("cause = %v, want workspace template bootstrap failure", got)
	}
}

func TestUpdateBotHTTPErrorMapsNameConflictToStableCode(t *testing.T) {
	err := updateBotHTTPError(bots.ErrBotNameTaken)
	if got := apperror.CodeOf(err); got != apperror.CodeBotNameTaken {
		t.Fatalf("code = %q, want %q", got, apperror.CodeBotNameTaken)
	}
}

// bridgeUnavailable is the error the bridge client returns when the
// workspace runtime cannot be reached.
func bridgeUnavailable() error {
	received := status.Error(codes.Unavailable, "connection refused")
	return rpc.Decode(received, nil, map[codes.Code]error{codes.Unavailable: bridge.ErrUnavailable})
}

func TestFSHTTPErrorKeepsUnavailableCausePrivate(t *testing.T) {
	cause := bridgeUnavailable()
	err := fsHTTPError(cause)
	if got := apperror.CodeOf(err); got != apperror.CodeWorkspaceUnreachable {
		t.Fatalf("code = %q, want %q", got, apperror.CodeWorkspaceUnreachable)
	}
	if got := apperror.CauseOf(err); !errors.Is(got, bridge.ErrUnavailable) {
		t.Fatalf("cause = %v, want bridge unavailable", got)
	}
	if got := errs.FaultOf(err); got != apperror.FaultDependency {
		t.Fatalf("fault = %q, want dependency", got)
	}
}

func TestFSHTTPErrorReturnsUnexpectedCause(t *testing.T) {
	cause := errors.New("grpc Internal: open /data/a.txt: input/output error")
	err := fsHTTPError(cause)
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want the cause without an HTTP status", err)
	}
	if got := errs.FaultOf(err); got != apperror.FaultServer {
		t.Fatalf("fault = %q, want server", got)
	}
}

func TestWorkspaceDependencyErrorAttributesUnreachableToDependency(t *testing.T) {
	err := workspaceDependencyError(bridgeUnavailable())
	if got := apperror.CodeOf(err); got != apperror.CodeWorkspaceUnreachable {
		t.Fatalf("code = %q, want %q", got, apperror.CodeWorkspaceUnreachable)
	}
	if got := errs.FaultOf(err); got != apperror.FaultDependency {
		t.Fatalf("fault = %q, want dependency", got)
	}
}

// The display error event is the stream error event with the failed step,
// flat on one JSON object as the Web client reads it.
func TestDisplayPrepareErrorEventIsAStreamErrorWithItsStep(t *testing.T) {
	frame, _ := server.NewStreamError(context.Background(), apperror.Wrap(apperror.CodeWorkspaceDisplayPrepareFailed, errors.New("rpc error: stream reset"), nil), "req-2")
	data, err := json.Marshal(displayPrepareErrorEvent{StreamError: frame, Step: "installing"})
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type": "error", "step": "installing", "code": string(apperror.CodeWorkspaceDisplayPrepareFailed),
		"message": "Display preparation failed.", "detail": "Display preparation failed.",
		"fault": string(apperror.FaultServer), "request_id": "req-2",
	}
	for key, value := range want {
		if event[key] != value {
			t.Fatalf("%s = %v, want %v in %s", key, event[key], value, data)
		}
	}
	if strings.Contains(string(data), "stream reset") {
		t.Fatal("private cause leaked into the stream event")
	}
}

func TestWorkspaceSetupFailureKeepsBootstrapDiagnosticPrivate(t *testing.T) {
	var event server.StreamError
	recorded := sendWorkspaceFailure(context.Background(), func(payload any) bool {
		event, _ = payload.(server.StreamError)
		return true
	}, botworkspace.Workspace{
		Observed:       botworkspace.ObservedFailed,
		LastErrorPhase: botworkspace.PhaseBootstrap,
		LastError:      "write /data/AGENTS.md: permission denied",
	}, "req-bootstrap")
	if event.Code != string(apperror.CodeWorkspaceTemplateBootstrapFailed) {
		t.Fatalf("code = %q", event.Code)
	}
	if event.Detail != "The workspace files could not be initialized." {
		t.Fatalf("detail = %q", event.Detail)
	}
	if event.Message != event.Detail {
		t.Fatalf("message = %q, detail = %q", event.Message, event.Detail)
	}
	if strings.Contains(event.Message, "/data/AGENTS.md") || strings.Contains(event.Detail, "/data/AGENTS.md") {
		t.Fatal("private workspace path leaked into SSE event")
	}
	if event.RequestID != "req-bootstrap" {
		t.Fatalf("request_id = %q", event.RequestID)
	}
	if apperror.CodeOf(recorded) != apperror.CodeWorkspaceTemplateBootstrapFailed || !strings.Contains(apperror.CauseOf(recorded).Error(), "/data/AGENTS.md") {
		t.Fatalf("recorded = %v, want %s carrying the backend's text", recorded, apperror.CodeWorkspaceTemplateBootstrapFailed)
	}
}
