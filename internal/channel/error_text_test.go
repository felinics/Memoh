package channel

import (
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/i18n"
)

// Copy rendered from a code is shown without the Error: label; a bare error
// text keeps it.
func TestErrorReplyTextPrefix(t *testing.T) {
	const copyText = "The model provider is overloaded right now. Please try again in a moment."
	if got := ErrorReplyText("agent.provider_overloaded", copyText); got != copyText {
		t.Fatalf("coded reply = %q", got)
	}
	if got := ErrorReplyText("", "boom"); got != "Error: boom" {
		t.Fatalf("uncoded reply = %q", got)
	}
}

func TestErrorCodeText(t *testing.T) {
	zh, ok := ErrorCodeText(i18n.New("zh"), apperror.CodeRuntimeRunFailed, nil)
	if !ok || zh != "回复未能完成，请重试。" {
		t.Fatalf("zh copy = %q, %v", zh, ok)
	}
	withArgs, ok := ErrorCodeText(i18n.New("en"), apperror.CodeAgentDependencyMissing, map[string]string{"dep_id": "codex"})
	if !ok || withArgs[:5] != "codex" {
		t.Fatalf("args copy = %q, %v", withArgs, ok)
	}
	detail, ok := ErrorCodeText(nil, apperror.CodeAgentProviderOverloaded, nil)
	if !ok || detail != "The model provider is overloaded right now. Please try again in a moment." {
		t.Fatalf("catalog detail = %q, %v", detail, ok)
	}
	if text, ok := ErrorCodeText(i18n.New("en"), "not.in_catalog", nil); ok {
		t.Fatalf("unknown code copy = %q", text)
	}
}

// A code without copy falls back to runtime_run_failed, never the code itself.
func TestRunFailureEventFallsBackToRunFailed(t *testing.T) {
	event := RunFailureEvent(i18n.New("en"), "not.in_catalog", nil)
	if event.Type != StreamEventError || event.ErrorCode != "runtime_run_failed" || event.Error != "The response could not be completed. Please try again." {
		t.Fatalf("event = %+v", event)
	}
	event = RunFailureEvent(i18n.New("en"), "", nil)
	if event.ErrorCode != "runtime_run_failed" {
		t.Fatalf("empty code event = %+v", event)
	}
}
