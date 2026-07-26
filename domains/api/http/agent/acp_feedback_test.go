package agent

import (
	"errors"
	"net/http"
	"testing"

	acpfeedback "github.com/memohai/memoh/domains/agent/decision/feedback"
	"github.com/memohai/memoh/internal/apperror"
)

func TestACPFeedbackHTTPErrorUsesContractKind(t *testing.T) {
	feedback := acpfeedback.New(
		acpfeedback.CodeNoWorkspaceExec,
		"missing_permission",
		http.StatusForbidden,
		"chat.acp.noWorkspaceExec",
		"raw backend message",
		map[string]string{"agent_id": "codex"},
	)
	err := AcpFeedbackHTTPError(feedback)
	if kind := apperror.KindOf(err); kind != apperror.KindForbidden {
		t.Fatalf("kind = %s, want %s", kind, apperror.KindForbidden)
	}
	if !errors.Is(err, feedback) {
		t.Fatalf("cause should unwrap to feedback error")
	}
}
