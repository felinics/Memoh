package contextview

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestMemoryCollectorPreservesLegacyMessageBytes(t *testing.T) {
	t.Parallel()

	raw := "  raw <memory> & hook text \n"
	frags, err := (&MemoryContextCollector{}).Collect(context.Background(), CollectRequest{Config: MemoryContextConfig{Text: raw}})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || frags[0].Kind != contextfrag.KindMemoryRecall {
		t.Fatalf("frags = %#v", frags)
	}
	msg := contextfrag.FragMessage(frags[0])
	text, ok := msg.Content[0].(sdk.TextPart)
	if !ok || text.Text != raw {
		t.Fatalf("memory text = %#v, want %q", msg.Content[0], raw)
	}
}
