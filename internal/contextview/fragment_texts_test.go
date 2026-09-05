package contextview

import (
	"context"
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

type recordingTextSink struct {
	texts []contextfrag.FragmentText
}

func (s *recordingTextSink) PersistFragmentTexts(texts []contextfrag.FragmentText) {
	s.texts = append(s.texts, texts...)
}

func TestApplyProviderRunConfigRecordsSelectedFragmentTexts(t *testing.T) {
	t.Parallel()
	sink := &recordingTextSink{}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetTextSink(sink)
	frags := append(prefixSystemFrags(), currentMessageFrag("message.000", "q1"))

	got := ApplyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{ContextSourceFrags: frags, ContextLifecycle: holder})

	hashes := map[string]contextfrag.Kind{}
	for _, item := range got.ContextManifest.Items {
		if item.Slot == contextfrag.SlotSystem {
			hashes[item.Ref.ContentHash] = item.Kind
		}
	}
	if len(sink.texts) != 2 || len(hashes) != 2 {
		t.Fatalf("texts = %#v, system items = %#v, want the two system fragments and no message", sink.texts, hashes)
	}
	for _, text := range sink.texts {
		if kind, ok := hashes[text.ContentHash]; !ok || kind != text.Kind || text.Text == "" || text.Label == "" || text.TextHash == "" {
			t.Fatalf("text %#v does not match a manifest item %#v", text, hashes)
		}
	}
}
