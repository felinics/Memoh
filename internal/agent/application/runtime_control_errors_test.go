package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
)

func TestRuntimeControlErrorMapsSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want apperror.Code
	}{
		{"canceled", context.Canceled, apperror.CodeRuntimeControlCancelled},
		{"forbidden", approval.ErrForbidden, apperror.CodeRuntimeControlForbidden},
		{"turn busy", fmt.Errorf("%w: thread t", turn.ErrSessionBusy), apperror.CodeSessionBusy},
		{"unsupported", external.ErrControlUnsupported, apperror.CodeRuntimeControlUnsupported},
		{"command unavailable", external.ErrCommandUnavailable, apperror.CodeRuntimeControlCommandUnavailable},
		{"mode unavailable", external.ErrModeUnavailable, apperror.CodeRuntimeControlModeUnavailable},
		{"thread unavailable", external.ErrThreadUnavailable, apperror.CodeRuntimeControlThreadUnavailable},
		{"auth required", fmt.Errorf("driver: %w", external.ErrAuthRequired), apperror.CodeExternalRuntimeAuthRequired},
		{"unknown", errors.New("boom"), apperror.CodeRuntimeControlFailed},
		// Only the turn sentinel leaves the session runtime adapter. The
		// session runtime values reaching this table unconverted are a bug in
		// the adapter, not a busy thread.
		{"session runtime busy", sessionruntime.ErrSessionBusy, apperror.CodeRuntimeControlFailed},
		{"session runtime history reset", sessionruntime.ErrHistoryResetInProgress, apperror.CodeRuntimeControlFailed},
		{"ledger history reset", ledger.ErrHistoryResetInProgress, apperror.CodeRuntimeControlFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := apperror.CodeOf(RuntimeControlError(tc.err)); got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRuntimeControlErrorKeepsExistingCode(t *testing.T) {
	existing := fmt.Errorf("wrapped: %w", apperror.New(apperror.CodeRuntimeControlRequestInvalid, nil))
	if got := RuntimeControlError(existing); !errors.Is(got, existing) {
		t.Fatalf("existing code was re-translated: %v", got)
	}
	if RuntimeControlError(nil) != nil {
		t.Fatal("nil error was translated")
	}
}

func TestPublicRuntimeControlErrorKeepsFeedbackAndTeamRouting(t *testing.T) {
	feedback := agentfeedback.New("acp_agent_not_found", "", http.StatusNotFound, "", "", nil)
	if got := publicRuntimeControlError(feedback); !errors.Is(got, feedback) {
		t.Fatalf("feedback was translated: %v", got)
	}
	if got := publicRuntimeControlError(turn.ErrTeamNotServed); !errors.Is(got, turn.ErrTeamNotServed) {
		t.Fatalf("team routing sentinel was translated: %v", got)
	}
}

func TestSentinelIdentities(t *testing.T) {
	if !errors.Is(sessionruntime.ErrSessionBusy, ledger.ErrSessionBusy) {
		t.Fatal("session runtime busy must stay an alias of the ledger value")
	}
	if errors.Is(sessionruntime.ErrSessionBusy, turn.ErrSessionBusy) || errors.Is(turn.ErrSessionBusy, sessionruntime.ErrSessionBusy) {
		t.Fatal("turn busy must stay a separate value from the session runtime port")
	}
	if errors.Is(sessionruntime.ErrHistoryResetInProgress, ledger.ErrHistoryResetInProgress) ||
		errors.Is(ledger.ErrHistoryResetInProgress, sessionruntime.ErrHistoryResetInProgress) {
		t.Fatal("history reset sentinels must stay two values")
	}
}

func TestRuntimeControlReportsBusySessionAsTurnBusy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*scriptedAdmitter)
	}{
		{"admission error", func(a *scriptedAdmitter) { a.admitErr = sessionruntime.ErrSessionBusy }},
		{"not started", func(a *scriptedAdmitter) { a.started = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, driver, admitter, req := runtimeControlFixture(t)
			tc.prepare(admitter)
			req.Command = "shrink"
			_, err := svc.ExecuteRuntimeCommand(t.Context(), req)
			if got := apperror.CodeOf(err); got != apperror.CodeSessionBusy {
				t.Fatalf("code = %q (%v), want %q", got, err, apperror.CodeSessionBusy)
			}
			if driver.compactCalls != 0 {
				t.Fatal("busy session dispatched the command")
			}
		})
	}
}
