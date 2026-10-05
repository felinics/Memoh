package acp

import (
	"errors"
	"fmt"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/acp/client"
)

func TestNormalizePromptErrorNamesTheMissingCommand(t *testing.T) {
	cause := fmt.Errorf("start devin acp: %w", &client.CommandNotFoundError{Command: "devin"})
	err := normalizePromptError(cause)
	var prompt *PromptError
	if !errors.As(err, &prompt) {
		t.Fatalf("normalizePromptError() = %T %v, want *PromptError", err, err)
	}
	var missing *client.CommandNotFoundError
	if !errors.As(prompt.Cause(), &missing) || missing.Command != "devin" {
		t.Fatalf("normalizePromptError() cause = %v, want the command the user must install", prompt.Cause())
	}
	if !errors.Is(prompt.Cause(), cause) {
		t.Fatalf("normalizePromptError() cause = %v, want private cause", prompt.Cause())
	}
	if errors.As(err, &missing) {
		t.Fatal("PromptError exposed its cause to errors.As")
	}
}
