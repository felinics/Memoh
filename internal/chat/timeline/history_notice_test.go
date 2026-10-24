package timeline

import (
	"slices"
	"strings"
	"testing"
	"time"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// The notice is model-visible: it pins how many messages the origin leaves
// out and a range of whole seconds that a search over it finds them all in.
func TestHistoryNoticeSaysWhatTheOriginLeavesOut(t *testing.T) {
	at := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	tokyo := time.FixedZone("JST", 9*60*60)
	for _, tc := range []struct {
		name   string
		origin messagepkg.HistoryOrigin
		want   string
	}{
		{name: "no origin"},
		{name: "an origin that leaves nothing out", origin: messagepkg.HistoryOrigin{At: at}},
		{
			name: "many messages",
			origin: messagepkg.HistoryOrigin{
				At:           at,
				OmittedCount: 12,
				OmittedFirst: time.Date(2026, 8, 1, 10, 0, 0, 250e6, time.UTC),
				OmittedLast:  time.Date(2026, 8, 2, 11, 30, 0, 750e6, time.UTC),
			},
			want: "[History notice] 12 earlier messages of this conversation, sent from 2026-08-01T10:00:00Z to 2026-08-02T11:30:01Z, are not in this context. " +
				"Only its conversation summaries, if any, carry what they said; to recall details, search the message history in that time range.",
		},
		{
			name: "one message within a second",
			origin: messagepkg.HistoryOrigin{
				At:           at,
				OmittedCount: 1,
				OmittedFirst: time.Date(2026, 8, 1, 19, 0, 0, 250e6, tokyo),
				OmittedLast:  time.Date(2026, 8, 1, 19, 0, 0, 250e6, tokyo),
			},
			want: "[History notice] 1 earlier message of this conversation, sent from 2026-08-01T10:00:00Z to 2026-08-01T10:00:01Z, is not in this context. " +
				"Only its conversation summaries, if any, carry what it said; to recall details, search the message history in that time range.",
		},
		{
			name: "messages sent the same whole second",
			origin: messagepkg.HistoryOrigin{
				At:           at,
				OmittedCount: 2,
				OmittedFirst: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
				OmittedLast:  time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
			},
			want: "[History notice] 2 earlier messages of this conversation, sent at 2026-08-01T10:00:00Z, are not in this context. " +
				"Only its conversation summaries, if any, carry what they said; to recall details, search the message history in that time range.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HistoryNotice(tc.origin); got != tc.want {
				t.Fatalf("notice = %q\nwant     %q", got, tc.want)
			}
		})
	}
}

func historyOriginAt(ms int64) messagepkg.HistoryOrigin {
	return messagepkg.HistoryOrigin{At: time.UnixMilli(ms), OmittedCount: 3, OmittedFirst: time.UnixMilli(1000), OmittedLast: time.UnixMilli(2500)}
}

// Composition reads nothing received or requested before the origin. The
// notice opens the history, ahead of a summary of what came before the
// origin, which still renders.
func TestComposeContextStartsAtTheHistoryOrigin(t *testing.T) {
	origin := historyOriginAt(3000)
	rc := RenderedContext{
		textSegment("m1", 1000, "before-input-1"),
		textSegment("m2", 2000, "before-input-2"),
		textSegment("m3", 3000, "after-input-1"),
		textSegment("m4", 4000, "after-input-2"),
	}
	trs := []TurnResponseEntry{assistantTR(2500, "before-reply"), assistantTR(3500, "after-reply")}
	artifacts := []CompactionArtifact{{
		ID:          "legacy",
		Summary:     "legacy summary",
		AnchorEndMs: 900,
		Sources:     []CompactionSource{{ExternalMessageID: "m0", CreatedAtMs: 900}},
	}}

	composed := ComposeContextWithArtifacts(rc, trs, artifacts, origin)
	if composed == nil || len(composed.Messages) != 5 {
		t.Fatalf("composed = %v, want the notice, the summary and the three entries from the origin on", composed)
	}
	texts := messageTexts(composed.Messages)
	if texts[0] != "system:"+HistoryNotice(origin) || texts[1] != "user:<summary>\nlegacy summary\n</summary>" {
		t.Fatalf("history opens with %q, want the notice and then the summary", texts[:2])
	}
	order := []string{"after-input-1", "after-reply", "after-input-2"}
	for i, want := range order {
		if !strings.Contains(texts[2+i], want) {
			t.Fatalf("entry %d = %q, want %s", 2+i, texts[2+i], want)
		}
	}
	if joined := strings.Join(texts, "\n"); strings.Contains(joined, "before-") {
		t.Fatalf("composition read history from before the origin: %q", texts)
	}

	// Without an origin the same history composes whole, without a notice.
	whole := messageTexts(ComposeContextWithArtifacts(rc, trs, artifacts, messagepkg.HistoryOrigin{}).Messages)
	if len(whole) != 7 || slices.ContainsFunc(whole, func(text string) bool { return strings.HasPrefix(text, "system:") }) {
		t.Fatalf("composition without an origin = %q", whole)
	}
	// Nothing from the origin on leaves nothing to open.
	if composed := ComposeContextWithArtifacts(rc[:2], trs[:1], nil, origin); composed != nil {
		t.Fatalf("composition before the origin = %q, want none", messageTexts(composed.Messages))
	}
}

// Under a tight budget the notice stays with the summaries while older
// history drops.
func TestComposeBudgetedKeepsTheHistoryNotice(t *testing.T) {
	origin := historyOriginAt(3000)
	rc := RenderedContext{
		textSegment("m3", 3000, strings.Repeat("a", 4000)),
		textSegment("m4", 4000, "newest input"),
	}
	composed, admission := ComposeContextWithArtifactsBudgeted(rc, nil, nil, origin, ComposeBudget{MaxTokens: 200})
	if composed == nil || admission.DroppedEntries != 1 || admission.ProtectedOverflow {
		t.Fatalf("composed=%v admission=%+v, want the older input dropped", composed, admission)
	}
	texts := messageTexts(composed.Messages)
	if len(texts) != 2 || texts[0] != "system:"+HistoryNotice(origin) || !strings.Contains(texts[1], "newest input") {
		t.Fatalf("composed = %q, want the notice and the newest input", texts)
	}
}
