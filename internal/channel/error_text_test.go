package channel

import (
	"errors"
	"fmt"
	"strings"
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

func TestErrorEvent(t *testing.T) {
	tests := []struct {
		name     string
		locale   string
		err      error
		wantText string
		// wantPrefix accepts copy that continues after wantText.
		wantPrefix bool
		wantCode   string
	}{
		{
			name:       "catalogued code renders its copy with args",
			locale:     "en",
			err:        fmt.Errorf("start turn: %w", apperror.New(apperror.CodeAgentDependencyMissing, map[string]string{"dep_id": "codex"})),
			wantText:   "codex",
			wantPrefix: true,
			wantCode:   string(apperror.CodeAgentDependencyMissing),
		},
		{
			name:     "localized copy follows the localizer",
			locale:   "zh",
			err:      apperror.New(apperror.CodeRuntimeRunFailed, nil),
			wantText: "回复未能完成，请重试。",
			wantCode: string(apperror.CodeRuntimeRunFailed),
		},
		{
			name:     "uncoded error keeps its own text",
			locale:   "en",
			err:      errors.New("resolve: model not found"),
			wantText: "resolve: model not found",
		},
		{
			name:     "code without copy keeps the error text and no code",
			locale:   "en",
			err:      apperror.New("not.in_catalog", nil),
			wantText: apperror.New("not.in_catalog", nil).Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := ErrorEvent(i18n.New(tt.locale), tt.err)
			if event.Type != StreamEventError || event.ErrorCode != tt.wantCode {
				t.Fatalf("event = %+v, want code %q", event, tt.wantCode)
			}
			matched := event.Error == tt.wantText
			if tt.wantPrefix {
				matched = strings.HasPrefix(event.Error, tt.wantText)
			}
			if !matched {
				t.Fatalf("text = %q, want %q", event.Error, tt.wantText)
			}
		})
	}
}
