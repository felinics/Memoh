package acp

import (
	"errors"
	"fmt"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/apperror"
)

func TestNormalizePromptErrorNamesTheMissingCommand(t *testing.T) {
	cause := fmt.Errorf("start devin acp: %w", &client.CommandNotFoundError{Command: "devin"})
	err := normalizePromptError(cause)
	if got := apperror.CodeOf(err); got != apperror.CodeACPCommandNotFound {
		t.Fatalf("normalizePromptError() code = %q, want %q", got, apperror.CodeACPCommandNotFound)
	}
	if got := apperror.ArgsOf(err)["command"]; got != "devin" {
		t.Fatalf("normalizePromptError() command arg = %q, want the command the user must install", got)
	}
	if got := apperror.CauseOf(err); !errors.Is(got, cause) {
		t.Fatalf("normalizePromptError() cause = %v, want private cause", got)
	}
}
