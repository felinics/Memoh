package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	acpfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
)

func TestACPFeedbackHTTPErrorPreservesStructuredPayload(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)

	feedback := acpfeedback.New(
		acpfeedback.CodeNoWorkspaceExec,
		"missing_permission",
		http.StatusForbidden,
		"chat.acp.noWorkspaceExec",
		"raw backend message",
		map[string]string{"agent_id": "codex"},
	)
	e.DefaultHTTPErrorHandler(echo.NewHTTPError(feedback.HTTPStatus, feedback), ctx)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["code"] != acpfeedback.CodeNoWorkspaceExec {
		t.Fatalf("code = %#v, want %q", got["code"], acpfeedback.CodeNoWorkspaceExec)
	}
	if got["i18n_key"] != "chat.acp.noWorkspaceExec" {
		t.Fatalf("i18n_key = %#v", got["i18n_key"])
	}
	args, ok := got["args"].(map[string]any)
	if !ok || args["agent_id"] != "codex" {
		t.Fatalf("args = %#v", got["args"])
	}
	if got["message"] != "raw backend message" {
		t.Fatalf("message = %#v", got["message"])
	}
}
