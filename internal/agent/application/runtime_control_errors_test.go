package application

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/external/acp"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/agentcredential"
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
		{"mode unavailable", external.Fail(external.FailureModeUnavailable, nil), apperror.CodeRuntimeControlModeUnavailable},
		{"thread unavailable", external.ErrThreadUnavailable, apperror.CodeRuntimeControlThreadUnavailable},
		{"auth required", fmt.Errorf("driver: %w", external.Fail(external.FailureAuthRequired, nil)), apperror.CodeExternalRuntimeAuthRequired},
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

// A runtime failure a control ran into keeps the code the External Agent
// translation gives it.
func TestRuntimeControlErrorKeepsRuntimeFailureCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want apperror.Code
	}{
		{"auth required", external.Fail(external.FailureAuthRequired, nil), apperror.CodeExternalRuntimeAuthRequired},
		{"credential revoked", external.CredentialError(agentcredential.ErrRevoked), apperror.CodeAgentCredentialRevoked},
		{"agent failed", external.Upstream(errors.New("bridge refused")), apperror.CodeExternalRuntimeReported},
		{"cancelled", external.Upstream(context.Canceled), apperror.CodeRuntimeControlCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := apperror.CodeOf(RuntimeControlError(fmt.Errorf("control: %w", tc.err))); got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPublicRuntimeControlErrorKeepsExternalAgentCodesAndTeamRouting(t *testing.T) {
	missing := fmt.Errorf("resolve: %w", acpagent.ErrAgentNotFound)
	if got := publicRuntimeControlError(missing); apperror.CodeOf(got) != apperror.CodeACPAgentNotFound {
		t.Fatalf("unknown agent = %v (code %q), want %q", got, apperror.CodeOf(got), apperror.CodeACPAgentNotFound)
	}
	if got := publicRuntimeControlError(turn.ErrTeamNotServed); !errors.Is(got, turn.ErrTeamNotServed) {
		t.Fatalf("team routing sentinel was translated: %v", got)
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
