package inbound

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/markdownmedia"
	"github.com/felinics/memoh/internal/media"
)

func TestBindPublishedMediaUsesSnapshotAndPreservesRoutingMetadata(t *testing.T) {
	source := "[report](/data/report.txt)"
	binding := markdownmedia.Binding{Reference: markdownmedia.Parse(source)[0], Asset: media.Asset{ContentHash: "original-snapshot"}}
	published := []messagepkg.Message{{Metadata: map[string]any{"markdown_media_source": source, markdownmedia.MetadataKey: []markdownmedia.Binding{binding}}}}
	msg := bindPublishedMedia(channel.Message{Text: source, Metadata: map[string]any{"existing": "keep"}}, &published, "zh")
	refs := markdownmedia.Bindings(msg.Metadata)
	if len(refs) != 1 || refs[0].Asset.ContentHash != "original-snapshot" || msg.Metadata["existing"] != "keep" || len(published) != 0 {
		t.Fatalf("msg=%+v", msg)
	}
	// The already consumed publication cannot silently reread a changed file.
	msg = bindPublishedMedia(channel.Message{Text: source}, &published, "zh")
	refs = markdownmedia.Bindings(msg.Metadata)
	if len(refs) != 1 || refs[0].ErrorCode != "media.reference_unavailable" {
		t.Fatalf("refs=%+v", refs)
	}
}
