package client

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	sdk "github.com/felinics/twilight/sdk"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/external/acp/profile"
	"github.com/felinics/memoh/internal/messageconv"
)

func TestPromptUsageFromACPMapsTokenDetails(t *testing.T) {
	cacheRead := 3
	cacheWrite := 2
	thought := 5

	got := promptUsageFromACP(&acp.Usage{
		InputTokens:       10,
		OutputTokens:      7,
		TotalTokens:       17,
		CachedReadTokens:  &cacheRead,
		CachedWriteTokens: &cacheWrite,
		ThoughtTokens:     &thought,
	})

	if got == nil {
		t.Fatal("usage = nil")
	}
	if got.InputTokens != 10 || got.OutputTokens != 7 || got.TotalTokens != 17 {
		t.Fatalf("usage totals = %+v", got)
	}
	if got.CachedInputTokens != 3 || got.InputTokenDetails.CacheReadTokens != 3 || got.InputTokenDetails.CacheWriteTokens != 2 {
		t.Fatalf("input token details = %+v", got)
	}
	if got.ReasoningTokens != 5 || got.OutputTokenDetails.ReasoningTokens != 5 {
		t.Fatalf("reasoning token details = %+v", got)
	}
}

// claude-agent-acp reports inputTokens without the cache counters and counts
// them in totalTokens. The #1374 sample must reach history as total input.
func TestPromptStoresACPCacheInsideInput(t *testing.T) {
	reported, err := json.Marshal(acp.Usage{InputTokens: 109845, OutputTokens: 500, TotalTokens: 109845 + 500 + 1129280, CachedReadTokens: acp.Ptr(1129280), CachedWriteTokens: acp.Ptr(0)})
	if err != nil {
		t.Fatal(err)
	}
	runner, agentPath := newStartSessionTestRunner(t)
	t.Setenv("MEMOH_ACP_FAKE_AGENT_USAGE", string(reported))
	sess, err := runner.StartSession(context.Background(), StartRequest{
		AgentID:     acpprofile.AgentACPID,
		BotID:       "bot-1",
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     10 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer func() { _ = sess.Close() }()

	result, err := sess.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	var stored []json.RawMessage
	for _, msg := range messageconv.SDKMessagesToModelMessages(result.Output) {
		if msg.Role == "assistant" && len(msg.Usage) > 0 {
			stored = append(stored, msg.Usage)
		}
	}
	if len(stored) != 1 {
		t.Fatalf("stored usages = %d, want 1", len(stored))
	}
	var got sdk.Usage
	if err := json.Unmarshal(stored[0], &got); err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != 1239125 || got.OutputTokens != 500 || got.TotalTokens != 1239625 ||
		got.InputTokenDetails.NoCacheTokens != 109845 || got.InputTokenDetails.CacheReadTokens != 1129280 || got.CachedInputTokens != 1129280 {
		t.Fatalf("stored usage = %s", stored[0])
	}
	if rate := float64(got.InputTokenDetails.CacheReadTokens) / float64(got.InputTokens) * 100; rate < 91.1 || rate >= 91.2 {
		t.Fatalf("cache hit rate = %.2f%%, want 91.1%%", rate)
	}
}
