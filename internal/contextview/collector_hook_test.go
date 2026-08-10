package contextview

import (
	"context"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

func TestHookCollectorPreservesLegacySystemAuthorityAndBytes(t *testing.T) {
	t.Parallel()

	raw := "[Hook Context: BeforePromptBuild]\n  exact hook text \n"
	frags, err := (&HookContextCollector{}).Collect(context.Background(), CollectRequest{Config: HookContextConfig{Text: raw}})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || frags[0].Role != sdk.MessageRoleSystem || frags[0].Slot != contextfrag.SlotSystem {
		t.Fatalf("hook frag = %#v", frags)
	}
	if frags[0].Priority <= 70 || frags[0].CacheClass != contextfrag.CacheNever || frags[0].Trust != contextfrag.TrustSystem {
		t.Fatalf("hook metadata = %#v", frags[0])
	}
	if len(frags[0].Parts) != 1 || frags[0].Parts[0].Text != raw {
		t.Fatalf("hook parts = %#v", frags[0].Parts)
	}
}
