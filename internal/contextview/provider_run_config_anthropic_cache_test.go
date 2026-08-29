package contextview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	anthropicmessages "github.com/felinics/twilight/provider/anthropic/messages"
	openaicompletions "github.com/felinics/twilight/provider/openai/completions"
	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/models"
)

func applyProviderRunConfigOK(ctx context.Context, _ any, cfg agentpkg.RunConfig) agentpkg.RunConfig {
	out, err := applyProviderRunConfig(ctx, nil, cfg)
	if err != nil {
		panic(err)
	}
	return out
}

func sdkMessagesJSONEqual(got, want any) bool {
	gotRaw, gotErr := json.Marshal(got)
	wantRaw, wantErr := json.Marshal(want)
	return gotErr == nil && wantErr == nil && string(gotRaw) == string(wantRaw)
}

func anthropicCacheTestModel() *sdk.Model {
	provider := anthropicmessages.New(anthropicmessages.WithAPIKey("test"))
	return provider.ChatModel("claude-test")
}

func openAICacheTestModel() *sdk.Model {
	provider := openaicompletions.New(openaicompletions.WithAPIKey("test"))
	return provider.ChatModel("gpt-test")
}

func lastTextCacheControl(msg sdk.Message) *sdk.CacheControl {
	if len(msg.Content) == 0 {
		return nil
	}
	text, ok := msg.Content[len(msg.Content)-1].(sdk.TextPart)
	if !ok {
		return nil
	}
	return text.CacheControl
}

// countCacheControlBreakpoints walks every message part and tool in the
// decorated payload and counts real cache_control occurrences, so a
// breakpoint applied somewhere other than the specific locations this test
// checks by hand cannot go unnoticed.
func countCacheControlBreakpoints(messages []sdk.Message, tools []sdk.Tool) int {
	count := 0
	for _, msg := range messages {
		for _, part := range msg.Content {
			switch p := part.(type) {
			case sdk.TextPart:
				if p.CacheControl != nil {
					count++
				}
			case sdk.ImagePart:
				if p.CacheControl != nil {
					count++
				}
			case sdk.FilePart:
				if p.CacheControl != nil {
					count++
				}
			}
		}
	}
	for _, tool := range tools {
		if tool.CacheControl != nil {
			count++
		}
	}
	return count
}

// TestApplyProviderRunConfigAnthropicMessageLevelBreakpoint proves the fix end
// to end: a StableMessageCount produced by the context view (history frags are
// now CacheStable) actually reaches models.ApplyPromptCacheWithPlan and lands a
// cache_control breakpoint on the last stable history message, in addition to
// the pre-existing system and last-tool breakpoints Anthropic already got.
func TestApplyProviderRunConfigAnthropicMessageLevelBreakpoint(t *testing.T) {
	t.Parallel()

	// The stable prefix must clear models.MinCacheablePrefixTokens or the
	// message-level breakpoint is (correctly) withheld as provably below the
	// provider's minimum cacheable prefix.
	cfg := agentpkg.RunConfig{
		System: strings.Repeat("stable system prompt ", 60),
		Messages: []sdk.Message{
			sdk.UserMessage(strings.Repeat("h1 ", 200)),
			sdk.AssistantMessage(strings.Repeat("h2 ", 200)),
		},
		Query: "current question",
		ContextScope: contextfrag.Scope{
			BotID:     "bot-1",
			SessionID: "session-1",
		},
	}

	got := applyProviderRunConfigOK(context.Background(), nil, cfg)

	if got.ContextCachePlan.StableMessageCount != 2 {
		t.Fatalf("stable message count = %d, want 2", got.ContextCachePlan.StableMessageCount)
	}

	tools := []sdk.Tool{{Name: "search"}}
	model := anthropicCacheTestModel()
	newSystem, newMessages, newTools, systemPrepended, _ := models.ApplyPromptCacheWithPlan(model, models.DefaultPromptCacheTTL, got.ContextCachePlan, got.System, got.Messages, tools)

	if !systemPrepended {
		t.Fatal("expected the system prompt to be promoted into a message for Anthropic")
	}
	if newSystem != "" {
		t.Fatalf("system should be cleared after promotion, got %q", newSystem)
	}
	systemCC := lastTextCacheControl(newMessages[0])
	if systemCC == nil {
		t.Fatal("promoted system message should carry a cache breakpoint")
	}

	shift := 0
	if systemPrepended {
		shift = 1
	}
	stableIndex := got.ContextCachePlan.StableMessageCount - 1 + shift
	stableCC := lastTextCacheControl(newMessages[stableIndex])
	if stableCC == nil {
		t.Fatalf("last stable history message (index %d) should carry a cache breakpoint", stableIndex)
	}

	if len(newTools) == 0 || newTools[len(newTools)-1].CacheControl == nil {
		t.Fatal("last tool should carry a cache breakpoint")
	}

	// Real traversal, not a hand-incremented counter that can only ever
	// equal the number of presence checks written above regardless of what
	// the payload actually contains: it would silently pass even if an
	// extra, unwanted breakpoint appeared somewhere else in the payload.
	const anthropicBreakpointLimit = 4
	total := countCacheControlBreakpoints(newMessages, newTools)
	if total != 3 {
		t.Fatalf("total cache_control breakpoints = %d, want exactly 3 (system + last-history-message + last-tool)", total)
	}
	if total > anthropicBreakpointLimit {
		t.Fatalf("total cache_control breakpoints = %d, exceeds Anthropic's hard limit of %d", total, anthropicBreakpointLimit)
	}
}

// TestApplyProviderRunConfigNonAnthropicUnaffected proves a non-Anthropic
// model sees zero change from the fix: a non-zero StableMessageCount is
// produced by the context view, but ApplyPromptCacheWithPlan only implements
// cache decoration for Anthropic Messages, so the rendered messages must stay
// byte-identical to the pre-cache input.
func TestApplyProviderRunConfigNonAnthropicUnaffected(t *testing.T) {
	t.Parallel()

	cfg := agentpkg.RunConfig{
		System: "stable system prompt",
		Messages: []sdk.Message{
			sdk.UserMessage("h1"),
			sdk.AssistantMessage("h2"),
		},
		Query: "current question",
		ContextScope: contextfrag.Scope{
			BotID:     "bot-1",
			SessionID: "session-1",
		},
	}

	got := applyProviderRunConfigOK(context.Background(), nil, cfg)
	if got.ContextCachePlan.StableMessageCount == 0 {
		t.Fatal("test fixture must produce a non-zero stable message count")
	}

	tools := []sdk.Tool{{Name: "search"}}
	model := openAICacheTestModel()
	newSystem, newMessages, newTools, systemPrepended, _ := models.ApplyPromptCacheWithPlan(model, models.DefaultPromptCacheTTL, got.ContextCachePlan, got.System, got.Messages, tools)

	if systemPrepended {
		t.Fatal("non-Anthropic models must not get system promotion")
	}
	if newSystem != got.System {
		t.Fatalf("system = %q, want unchanged %q", newSystem, got.System)
	}
	if !sdkMessagesJSONEqual(newMessages, got.Messages) {
		t.Fatal("messages should be byte-identical to input for a non-Anthropic model")
	}
	for i, tool := range newTools {
		if tool.CacheControl != nil {
			t.Fatalf("tool %d should not carry a cache breakpoint for a non-Anthropic model", i)
		}
	}
}
