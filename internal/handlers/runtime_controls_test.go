package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
)

func TestRuntimeControlErrorTranslatesDriverSentinels(t *testing.T) {
	// bot_agents reads controls from a driver directly, so the handler is the
	// only place these sentinels are translated on that path.
	cases := []struct {
		err  error
		want apperror.Code
	}{
		{external.ErrControlUnsupported, apperror.CodeRuntimeControlUnsupported},
		{fmt.Errorf("read: %w", external.ErrAuthRequired), apperror.CodeExternalRuntimeAuthRequired},
		{turn.ErrSessionBusy, apperror.CodeSessionBusy},
		{sessionruntime.ErrSessionBusy, apperror.CodeRuntimeControlFailed},
		{echo.NewHTTPError(http.StatusBadGateway, "upstream"), apperror.CodeRuntimeControlFailed},
	}
	for _, tc := range cases {
		if got := apperror.CodeOf(runtimeControlError(tc.err)); got != tc.want {
			t.Errorf("runtimeControlError(%v) code = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestRuntimeControlErrorPassesThroughHTTPOutcomes(t *testing.T) {
	existing := apperror.New(apperror.CodeRuntimeControlModeUnavailable, nil)
	if got := runtimeControlError(existing); !errors.Is(got, existing) {
		t.Fatalf("existing code was re-translated: %v", got)
	}
	notFound := echo.NewHTTPError(http.StatusNotFound, "session not found")
	if got := runtimeControlError(notFound); !errors.Is(got, notFound) {
		t.Fatalf("client echo error was translated: %v", got)
	}
	feedback := agentfeedback.New("acp_agent_not_found", "", http.StatusNotFound, "", "", nil)
	var httpErr *echo.HTTPError
	if got := runtimeControlError(feedback); !errors.As(got, &httpErr) || httpErr.Code != http.StatusNotFound {
		t.Fatalf("feedback was not rendered as HTTP error: %v", got)
	}
}
