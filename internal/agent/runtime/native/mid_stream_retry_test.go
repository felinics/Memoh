package native

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestPrepareMidStreamRetryConfigKeepsConversationPrefix(t *testing.T) {
	cfg := RunConfig{
		System:   "sys",
		Messages: []sdk.Message{sdk.UserMessage("hello")},
	}
	accumulated := []sdk.Message{sdk.AssistantMessage("partial answer")}
	out := prepareMidStreamRetryConfig(cfg, accumulated, "api error 500")
	if len(out.Messages) != 2 {
		t.Fatalf("messages = %d, want input prefix + accumulated output", len(out.Messages))
	}
	if out.Messages[0].Role != sdk.MessageRoleUser {
		t.Fatalf("messages[0].Role = %s, want preserved user prefix", out.Messages[0].Role)
	}
	if out.Messages[1].Role != sdk.MessageRoleAssistant {
		t.Fatalf("messages[1].Role = %s, want accumulated assistant output", out.Messages[1].Role)
	}
}

func TestPrepareMidStreamRetryConfigStepZeroRetriesFromStart(t *testing.T) {
	cfg := RunConfig{Messages: []sdk.Message{sdk.UserMessage("hello")}}
	out := prepareMidStreamRetryConfig(cfg, nil, "timeout")
	if len(out.Messages) != 1 || out.Messages[0].Role != sdk.MessageRoleUser {
		t.Fatalf("messages = %+v, want original conversation unchanged", out.Messages)
	}
}

func TestPrepareMidStreamRetryConfigDoesNotMutateOriginal(t *testing.T) {
	original := []sdk.Message{sdk.UserMessage("hello")}
	cfg := RunConfig{Messages: original}
	_ = prepareMidStreamRetryConfig(cfg, []sdk.Message{sdk.AssistantMessage("partial")}, "timeout")
	if len(original) != 1 {
		t.Fatalf("original messages mutated: %d entries", len(original))
	}
}

func TestPrepareMidStreamRetryConfigPreservesPostViewMemoryManifest(t *testing.T) {
	cfg := postViewMemoryConfig()
	out := prepareMidStreamRetryConfig(cfg, []sdk.Message{sdk.AssistantMessage("partial")}, "timeout")

	if len(out.Messages) != 2 {
		t.Fatalf("messages = %d, want memory payload plus accumulated output", len(out.Messages))
	}
	assertSinglePostViewMemoryFrag(t, out)
}

func TestPrepareMidStreamRetryConfigDoesNotPersistRawError(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	secret := "provider failed authorization=Bearer secret-token"
	_ = prepareMidStreamRetryConfig(RunConfig{ContextMutations: ledger}, nil, secret)

	records := ledger.Records()
	if len(records) != 1 {
		t.Fatalf("mutation records = %#v, want one retry", records)
	}
	if strings.Contains(records[0].Detail, secret) || strings.Contains(records[0].Detail, "secret-token") {
		t.Fatalf("retry mutation leaked raw provider error: %q", records[0].Detail)
	}
	if !strings.Contains(records[0].Detail, "error_sha256=") {
		t.Fatalf("retry mutation missing error fingerprint: %q", records[0].Detail)
	}
}
