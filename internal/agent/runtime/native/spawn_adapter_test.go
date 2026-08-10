package native

import (
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/agent/sessionmode"
	tools "github.com/memohai/memoh/internal/agent/tool"
)

func TestSpawnRunConfigClassifiesRawQueryAsCurrentUser(t *testing.T) {
	t.Parallel()
	const query = "  keep raw query bytes  "
	rc := runConfigFromSpawnRunConfig(tools.SpawnRunConfig{
		System: SpawnSystemPrompt(sessionmode.Subagent), Query: query, SessionType: sessionmode.Subagent,
		Messages: []sdk.Message{sdk.UserMessage("history")},
	})

	if rc.ContextCurrentUserMessageIndex == nil || *rc.ContextCurrentUserMessageIndex != 1 {
		t.Fatalf("current user index = %#v, want 1", rc.ContextCurrentUserMessageIndex)
	}
	if text, _ := rc.Messages[1].Content[0].(sdk.TextPart); text.Text != query {
		t.Fatalf("spawn query = %q, want byte-exact %q", text.Text, query)
	}
	current := 0
	for _, frag := range rc.ContextSourceFrags {
		if frag.Slot == contextfrag.SlotCurrentUser {
			current++
			if frag.Kind != contextfrag.KindCurrentUserMessage {
				t.Fatalf("current fragment kind = %q", frag.Kind)
			}
		}
	}
	if current != 1 {
		t.Fatalf("current fragment count = %d, want 1", current)
	}
}

func TestSpawnContextSourceFragsDefersCustomSystemToFallback(t *testing.T) {
	t.Parallel()
	rc := RunConfig{System: "  custom spawn system\n", SessionType: sessionmode.Subagent}
	if got := SpawnContextSourceFrags(rc); got != nil {
		t.Fatalf("custom system source fragments = %#v, want legacy fallback", got)
	}
}
