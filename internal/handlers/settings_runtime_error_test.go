package handlers

import (
	"errors"
	"fmt"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/server"
	"github.com/felinics/memoh/internal/settings"
)

func TestSettingsRuntimeHTTPError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err    error
		code   apperror.Code
		status int
	}{
		{settings.ErrInvalidChatRuntime, apperror.CodeInvalidChatRuntime, 400},
		{settings.ErrACPProjectModeInvalid, apperror.CodeACPProjectModeInvalid, 400},
		{settings.ErrACPProjectPathInvalid, apperror.CodeACPProjectPathInvalid, 400},
		{settings.ErrACPUnknownAgent, apperror.CodeACPAgentNotFound, 400},
		{settings.ErrACPAgentNotEnabled, apperror.CodeACPAgentNotEnabled, 403},
		{settings.ErrACPAgentNotConfigured, apperror.CodeACPAgentNotConfigured, 400},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			t.Parallel()
			got := settingsRuntimeHTTPError(fmt.Errorf("update: %w", tc.err))
			problem, _ := server.ProblemFrom(got, "")
			if apperror.CodeOf(got) != tc.code || problem.Status != tc.status {
				t.Fatalf("error = %v (status %d), want %s %d", got, problem.Status, tc.code, tc.status)
			}
		})
	}
	if got := settingsRuntimeHTTPError(errors.New("db down")); got != nil {
		t.Fatalf("an unrelated error was translated: %v", got)
	}
}
