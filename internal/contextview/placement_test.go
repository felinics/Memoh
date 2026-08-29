package contextview

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestStablePrefixPlacerMarksContiguousStablePrefix(t *testing.T) {
	t.Parallel()

	dynamic := textFrag("dynamic", contextfrag.SlotSystem, contextfrag.KindToolUsage, sdk.MessageRoleSystem, "runtime tools")
	dynamic.CacheClass = contextfrag.CacheDynamic
	frags := contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{
		textFrag("system", contextfrag.SlotSystem, contextfrag.KindSystemPrompt, sdk.MessageRoleSystem, "system"),
		textFrag("identity", contextfrag.SlotSystem, contextfrag.KindBotIdentity, sdk.MessageRoleSystem, "identity"),
		dynamic,
		messageFrag("latest", sdk.UserMessage("latest")),
	})

	plan := StablePrefixPlacer{}.Place(frags, contextfrag.IntentRunConfigPreProvider)

	if plan.FirstVolatileIndex != 2 {
		t.Fatalf("FirstVolatileIndex = %d, want 2", plan.FirstVolatileIndex)
	}
	if plan.StablePrefixHash == "" {
		t.Fatal("StablePrefixHash should be set for stable prefix")
	}
	if len(plan.Items) != len(frags) {
		t.Fatalf("items = %d, want %d", len(plan.Items), len(frags))
	}
	for i, item := range plan.Items {
		if item.Position != i || item.FragID != frags[i].ID || item.CacheHint != frags[i].CacheClass {
			t.Fatalf("item %d = %#v, want position/id/cache from frag %#v", i, item, frags[i])
		}
	}
}

func TestStablePrefixPlacerHashIgnoresVolatileSuffix(t *testing.T) {
	t.Parallel()

	stable := textFrag("system", contextfrag.SlotSystem, contextfrag.KindSystemPrompt, sdk.MessageRoleSystem, "system")
	firstVolatile := messageFrag("latest", sdk.UserMessage("latest"))
	changedVolatile := messageFrag("latest", sdk.UserMessage("changed"))
	firstVolatile.CacheClass = contextfrag.CacheNever
	changedVolatile.CacheClass = contextfrag.CacheNever

	first := StablePrefixPlacer{}.Place(contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{stable, firstVolatile}), contextfrag.IntentRunConfigPreProvider)
	second := StablePrefixPlacer{}.Place(contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{stable, changedVolatile}), contextfrag.IntentRunConfigPreProvider)

	if first.StablePrefixHash == "" {
		t.Fatal("StablePrefixHash should be set")
	}
	if first.StablePrefixHash != second.StablePrefixHash {
		t.Fatalf("StablePrefixHash changed after volatile suffix edit: first=%q second=%q", first.StablePrefixHash, second.StablePrefixHash)
	}

	changedStable := textFrag("system", contextfrag.SlotSystem, contextfrag.KindSystemPrompt, sdk.MessageRoleSystem, "changed system")
	third := StablePrefixPlacer{}.Place(contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{changedStable, firstVolatile}), contextfrag.IntentRunConfigPreProvider)
	if first.StablePrefixHash == third.StablePrefixHash {
		t.Fatalf("StablePrefixHash did not change after stable prefix edit: %q", first.StablePrefixHash)
	}
}
