package acp

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
)

func TestPromptFailureShowsWhatTheAgentSaid(t *testing.T) {
	answer := &acp.RequestError{Code: -32603, Message: "Internal error", Data: map[string]any{"message": "model quota exhausted"}}
	var failure *external.Failure
	if err := promptFailure(fmt.Errorf("prompt: %w", answer)); !errors.As(err, &failure) || failure.Kind != external.FailureReported || failure.Message != "model quota exhausted" {
		t.Fatalf("promptFailure() = %v, want the agent's own words", err)
	}
	missing := fmt.Errorf("start devin acp: %w", &client.CommandNotFoundError{Command: "devin"})
	if err := promptFailure(missing); !errors.As(err, &failure) || failure.Kind != external.FailureReported || !strings.Contains(failure.Message, "devin") {
		t.Fatalf("promptFailure() = %v, want the missing command named", err)
	}
	if err := promptFailure(client.ErrModelUnavailable); !external.IsFailureKind(err, external.FailureModeUnavailable) {
		t.Fatalf("promptFailure() = %v, want a model the agent does not offer refused", err)
	}
	if err := promptFailure(ErrAgentNotEnabled); !errors.Is(err, ErrAgentNotEnabled) {
		t.Fatalf("promptFailure() = %v, want a setup rule kept for its own code", err)
	}
}
