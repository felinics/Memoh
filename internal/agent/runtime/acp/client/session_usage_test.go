package client

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	sdk "github.com/felinics/twilight/sdk"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/agent/toolexec"
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

func TestAttachUsageToLastAssistant(t *testing.T) {
	usage := &sdk.Usage{InputTokens: 10, OutputTokens: 7, TotalTokens: 17}
	output := []sdk.Message{
		sdk.UserMessage("question"),
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "first"}}},
		{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "exec", Result: toolexec.OutputFromValue("ok")}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "final"}}},
	}

	got := attachUsageToLastAssistant(output, usage)

	if got[1].Usage != nil {
		t.Fatalf("first assistant usage = %+v, want nil", got[1].Usage)
	}
	if got[3].Usage != usage {
		t.Fatalf("final assistant usage = %+v, want mapped usage", got[3].Usage)
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

func TestPromptUsageFromACPKeepsCacheWithinInput(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		input, output, total int
		read, write          int
		wantInput            int
		wantNoCache          int
	}{
		{name: "cache beside input", input: 10, output: 7, total: 317, read: 200, write: 100, wantInput: 310, wantNoCache: 10},
		{name: "cache beside larger input", input: 1000, output: 100, total: 1400, read: 300, wantInput: 1300, wantNoCache: 1000},
		{name: "cache within input", input: 310, output: 7, total: 317, read: 200, write: 100, wantInput: 310, wantNoCache: 10},
		{name: "cache larger than input", input: 10, output: 7, total: 17, read: 200, wantInput: 210, wantNoCache: 10},
		{name: "total fits neither accounting", input: 310, output: 7, total: 400, read: 200, wantInput: 310, wantNoCache: 110},
		{name: "no cache", input: 10, output: 7, total: 17, wantInput: 10, wantNoCache: 10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := promptUsageFromACP(&acp.Usage{InputTokens: tt.input, OutputTokens: tt.output, TotalTokens: tt.total, CachedReadTokens: acp.Ptr(tt.read), CachedWriteTokens: acp.Ptr(tt.write)})
			detail := got.InputTokenDetails
			if got.InputTokens != tt.wantInput || detail.NoCacheTokens != tt.wantNoCache || detail.CacheReadTokens != tt.read || detail.CacheWriteTokens != tt.write ||
				detail.NoCacheTokens+detail.CacheReadTokens+detail.CacheWriteTokens != got.InputTokens || got.CachedInputTokens != tt.read {
				t.Fatalf("usage = %+v", got)
			}
		})
	}
}
