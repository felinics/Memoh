package inbound

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/markdownmedia"
	"github.com/felinics/memoh/internal/media"
)

func TestBindFinalMediaUsesSnapshotAndPreservesRoutingMetadata(t *testing.T) {
	source := "[report](/data/report.txt)"
	binding := markdownmedia.Binding{Reference: markdownmedia.Parse(source)[0], Asset: media.Asset{ContentHash: "original-snapshot"}}
	published := []messagepkg.Message{{Metadata: map[string]any{"markdown_media_source": source, markdownmedia.MetadataKey: []markdownmedia.Binding{binding}}}}
	msg, earlier := bindFinalMedia(channel.Message{Text: source, Metadata: map[string]any{"existing": "keep"}}, true, published, "zh")
	refs := markdownmedia.Bindings(msg.Metadata)
	if len(refs) != 1 || refs[0].Asset.ContentHash != "original-snapshot" || msg.Metadata["existing"] != "keep" || len(earlier) != 0 {
		t.Fatalf("msg=%+v earlier=%+v", msg, earlier)
	}
	// Without a snapshot the reference fails instead of rereading a changed file.
	msg, _ = bindFinalMedia(channel.Message{Text: source}, true, nil, "zh")
	refs = markdownmedia.Bindings(msg.Metadata)
	if len(refs) != 1 || refs[0].ErrorCode != "media.reference_unavailable" {
		t.Fatalf("refs=%+v", refs)
	}
}

func TestEarlierStepMediaIsCarriedToTheFinal(t *testing.T) {
	messages := []turn.ModelMessage{
		{Role: "assistant", Content: json.RawMessage(`"Created [report](/data/r.txt), now checking."`), ToolCalls: []turn.ToolCall{{Type: "function", Function: turn.ToolCallFunction{Name: "exec", Arguments: `{}`}}}},
		{Role: "tool", Content: json.RawMessage(`"ok"`)},
		{Role: "assistant", Content: json.RawMessage(`"All tests pass."`)},
	}
	if !runPublishesMarkdownMedia(messages) {
		t.Fatal("text written before a tool call was not considered")
	}
	if runPublishesMarkdownMedia(messages[1:]) {
		t.Fatal("run without references loads snapshots")
	}
	ref := markdownmedia.Parse("[report](/data/r.txt)")[0]
	step := messagepkg.Message{Metadata: map[string]any{
		"markdown_media_source":   "Created [report](/data/r.txt), now checking.",
		markdownmedia.MetadataKey: []markdownmedia.Binding{{Reference: ref, Asset: media.Asset{ContentHash: "step-one"}}},
	}}
	final, earlier := bindFinalMedia(channel.Message{Text: "All tests pass."}, true, []messagepkg.Message{step}, "en")
	final = withEarlierMedia(final, earlier, "en")
	earlier = markdownmedia.Bindings(map[string]any{markdownmedia.MetadataKey: final.Metadata[markdownmedia.EarlierMetadataKey]})
	if len(earlier) != 1 || earlier[0].Asset.ContentHash != "step-one" || len(markdownmedia.Bindings(final.Metadata)) != 0 {
		t.Fatalf("final=%+v", final)
	}
}

type publishedMessages struct{ messages []messagepkg.Message }

func (*publishedMessages) Persist(context.Context, messagepkg.PersistInput) (messagepkg.Message, error) {
	return messagepkg.Message{}, nil
}

func (p *publishedMessages) ListSinceBySession(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return p.messages, nil
}

// An approval or ask_user continuation does not know its run id; it binds
// the reply to what the session published since the continuation started,
// never rereading workspace files in the Channel process.
func TestContinuationReplyBindsPublishedSnapshots(t *testing.T) {
	source := "![c](/data/c.png)"
	binding := markdownmedia.Binding{Reference: markdownmedia.Parse(source)[0], Asset: media.Asset{ContentHash: "continued"}}
	store := &publishedMessages{messages: []messagepkg.Message{
		{Metadata: map[string]any{"role": "user"}},
		{Metadata: map[string]any{"markdown_media_run_id": "run-2", markdownmedia.MetadataKey: []markdownmedia.Binding{binding}}},
	}}
	p := &ChannelInboundProcessor{message: store}
	finalMessages := []turn.ModelMessage{{Role: "assistant", Content: json.RawMessage(`"![c](/data/c.png)"`)}}
	msg, ok := p.bindRunMedia(context.Background(), "telegram", finalMessages, channel.Message{Text: source}, true, "session-1", "", time.Now(), "bot")
	refs := markdownmedia.Bindings(msg.Metadata)
	if !ok || len(refs) != 1 || refs[0].Asset.ContentHash != "continued" {
		t.Fatalf("ok=%v refs=%+v", ok, refs)
	}
	// Web renders from history and opens workspace links; it is never bound.
	if msg, _ := p.bindRunMedia(context.Background(), "web", finalMessages, channel.Message{Text: source}, true, "session-1", "", time.Now(), "bot"); msg.Metadata != nil {
		t.Fatalf("web reply bound: %+v", msg)
	}
}
