package acp

import (
	"errors"
	"fmt"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
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

func TestNormalizePromptErrorReportsWhatTheAgentSaid(t *testing.T) {
	answer := &acp.RequestError{Code: -32603, Message: "Internal error", Data: map[string]any{"message": "model quota exhausted"}}
	err := normalizePromptError(fmt.Errorf("prompt: %w", answer))
	var failure *external.Failure
	if !errors.As(err, &failure) || failure.Kind != external.FailureReported || failure.Message != "model quota exhausted" {
		t.Fatalf("normalizePromptError() = %v, want the agent's own words", err)
	}
	plain := errors.New("connection reset")
	if got := normalizePromptError(plain); !errors.Is(got, plain) || external.IsFailure(got) {
		t.Fatalf("normalizePromptError() = %v, want an error the agent did not answer unchanged", got)
	}
}
