package native

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestPrepareMidStreamRetryConfigWithMessagesCopiesInput(t *testing.T) {
	t.Parallel()

	original := []sdk.Message{sdk.UserMessage("hello")}
	out := prepareMidStreamRetryConfigWithMessages(RunConfig{}, original, nil, 0, "timeout")
	if len(out.Messages) != 1 {
		t.Fatalf("messages = %d, want the retry input", len(out.Messages))
	}
	out.Messages[0] = sdk.AssistantMessage("changed")
	if original[0].Role != sdk.MessageRoleUser {
		t.Fatalf("retry config aliases the caller's message slice: %+v", original)
	}
}

func TestPrepareMidStreamRetryConfigWithMessagesPreservesPostViewMemoryManifest(t *testing.T) {
	t.Parallel()

	cfg := postViewMemoryConfig()
	messages := append(append([]sdk.Message(nil), cfg.Messages...), sdk.AssistantMessage("partial"))
	out := prepareMidStreamRetryConfigWithMessages(cfg, messages, nil, 1, "timeout")

	if len(out.Messages) != 2 {
		t.Fatalf("messages = %d, want memory payload plus accumulated output", len(out.Messages))
	}
	assertSinglePostViewMemoryFrag(t, out)
}

func TestPrepareMidStreamRetryConfigWithMessagesDoesNotPersistRawError(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	secret := "provider failed authorization=Bearer secret-token"
	_ = prepareMidStreamRetryConfigWithMessages(RunConfig{ContextMutations: ledger}, nil, nil, 0, secret)

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
