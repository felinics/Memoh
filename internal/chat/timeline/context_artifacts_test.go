package timeline

import (
	"reflect"
	"strings"
	"testing"
)

func textSegment(id string, atMs int64, text string) RenderedSegment {
	return RenderedSegment{
		MessageID:    id,
		ReceivedAtMs: atMs,
		Content:      []RenderedContentPiece{{Type: "text", Text: text}},
	}
}

func assistantTR(atMs int64, text string) TurnResponseEntry {
	return TurnResponseEntry{RequestedAtMs: atMs, Role: "assistant", Content: text}
}

func messageTexts(messages []ContextMessage) []string {
	texts := make([]string, 0, len(messages))
	for _, m := range messages {
		texts = append(texts, m.Role+":"+m.Content)
	}
	return texts
}

func TestComposeContextWithArtifactsNoArtifactsMatchesComposeContext(t *testing.T) {
	rc := RenderedContext{textSegment("m1", 1000, "hello"), textSegment("m2", 2000, "world")}
	trs := []TurnResponseEntry{assistantTR(1500, "reply")}

	plain := ComposeContext(rc, trs)
	withArtifacts := ComposeContextWithArtifacts(rc, trs, nil)

	if plain == nil || withArtifacts == nil {
		t.Fatalf("expected non-nil results, got %v and %v", plain, withArtifacts)
	}
	if len(plain.Messages) != len(withArtifacts.Messages) {
		t.Fatalf("expected same message count, got %d vs %d", len(plain.Messages), len(withArtifacts.Messages))
	}
	for i := range plain.Messages {
		if !reflect.DeepEqual(plain.Messages[i], withArtifacts.Messages[i]) {
			t.Fatalf("message %d differs: %+v vs %+v", i, plain.Messages[i], withArtifacts.Messages[i])
		}
	}
}

func TestComposeContextWithArtifactsReplacesCoveredSlot(t *testing.T) {
	rc := RenderedContext{
		textSegment("m1", 1000, "old-1"),
		textSegment("m2", 2000, "old-2"),
		textSegment("m3", 3000, "new"),
	}
	trs := []TurnResponseEntry{assistantTR(2500, "reply")}
	artifacts := []CompactionArtifact{{
		ID:          "a1",
		Summary:     "summarized old",
		AnchorEndMs: 2000,
		Sources: []CompactionSource{
			{ExternalMessageID: "m1", CreatedAtMs: 1000},
			{ExternalMessageID: "m2", CreatedAtMs: 2000},
		},
	}}

	composed := ComposeContextWithArtifacts(rc, trs, artifacts)
	if composed == nil {
		t.Fatal("expected composed result")
	}
	if len(composed.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(composed.Messages), messageTexts(composed.Messages))
	}
	summary := composed.Messages[0]
	if summary.Role != "user" || summary.CompactionArtifactID != "a1" {
		t.Fatalf("expected leading summary from a1, got %+v", summary)
	}
	if summary.Content != "<summary>\nsummarized old\n</summary>" {
		t.Fatalf("unexpected summary content %q", summary.Content)
	}
	if strings.Contains(summary.Content+composed.Messages[1].Content+composed.Messages[2].Content, "old-1") {
		t.Fatal("covered original content must not remain in context")
	}
	if composed.Messages[1].Role != "assistant" {
		t.Fatalf("expected assistant reply second, got %+v", composed.Messages[1])
	}
	if composed.Messages[2].Role != "user" || !strings.Contains(composed.Messages[2].Content, "new") {
		t.Fatalf("expected uncovered message last, got %+v", composed.Messages[2])
	}
}

func TestComposeContextWithArtifactsAnchorFallback(t *testing.T) {
	rc := RenderedContext{textSegment("m3", 3000, "new")}
	artifacts := []CompactionArtifact{{
		ID:          "a1",
		Summary:     "aged out coverage",
		AnchorEndMs: 1000,
		Sources:     []CompactionSource{{ExternalMessageID: "m1", CreatedAtMs: 1000}},
	}}

	composed := ComposeContextWithArtifacts(rc, nil, artifacts)
	if composed == nil || len(composed.Messages) != 2 {
		t.Fatalf("expected summary + new message, got %+v", composed)
	}
	if composed.Messages[0].CompactionArtifactID != "a1" {
		t.Fatalf("expected summary first, got %+v", composed.Messages[0])
	}

	noAnchor := []CompactionArtifact{{ID: "a2", Summary: "no anchor"}}
	composed = ComposeContextWithArtifacts(rc, nil, noAnchor)
	if composed == nil || len(composed.Messages) != 2 || composed.Messages[0].CompactionArtifactID != "a2" {
		t.Fatalf("expected anchorless summary first, got %+v", composed)
	}
}

// A summary that folds work on both sides of an input it keeps renders right
// after the last source it covers, whichever of its sources the next request
// loads: the input stays before it, the uncovered tail after it.
func TestComposeContextWithArtifactsPlacesSummaryAfterItsLastSource(t *testing.T) {
	old, input := textSegment("m-old", 1000, "old history"), textSegment("m-input", 2000, "kept input")
	work := assistantTR(3000, "covered work")
	work.SourceMessageID = "h-work"
	tail := assistantTR(4000, "uncovered tail")
	tail.SourceMessageID = "h-tail"
	artifacts := []CompactionArtifact{{
		ID:          "a1",
		Summary:     "old history and the work after the input",
		AnchorEndMs: 3000,
		Sources: []CompactionSource{
			{ExternalMessageID: "m-old", HistoryMessageID: "h-old", CreatedAtMs: 1000},
			{HistoryMessageID: "h-work", CreatedAtMs: 3000},
		},
	}}
	want := []string{"user:kept input", "user:<summary>\nold history and the work after the input\n</summary>", "assistant:uncovered tail"}
	for _, loaded := range []struct {
		name string
		rc   RenderedContext
		trs  []TurnResponseEntry
	}{
		{name: "every source", rc: RenderedContext{old, input}, trs: []TurnResponseEntry{work, tail}},
		{name: "the last source", rc: RenderedContext{input}, trs: []TurnResponseEntry{work, tail}},
		{name: "an earlier source", rc: RenderedContext{old, input}, trs: []TurnResponseEntry{tail}},
		{name: "no source", rc: RenderedContext{input}, trs: []TurnResponseEntry{tail}},
	} {
		t.Run(loaded.name, func(t *testing.T) {
			composed := ComposeContextWithArtifacts(loaded.rc, loaded.trs, artifacts)
			if composed == nil || !reflect.DeepEqual(messageTexts(composed.Messages), want) {
				t.Fatalf("got %q, want %q", messageTexts(composed.Messages), want)
			}
		})
	}
}

func TestComposeContextWithArtifactsFiltersCoveredTurnResponses(t *testing.T) {
	tr1 := assistantTR(1500, "covered reply")
	tr1.SourceMessageID = "h1"
	tr2 := assistantTR(2500, "live reply")
	tr2.SourceMessageID = "h2"
	artifacts := []CompactionArtifact{{
		ID:      "a1",
		Summary: "covers h1",
		Sources: []CompactionSource{{HistoryMessageID: "h1"}},
	}}

	composed := ComposeContextWithArtifacts(nil, []TurnResponseEntry{tr1, tr2}, artifacts)
	if composed == nil || len(composed.Messages) != 2 {
		t.Fatalf("expected summary + live reply, got %+v", composed)
	}
	for _, m := range composed.Messages {
		if strings.Contains(m.Content, "covered reply") {
			t.Fatal("covered turn response must be dropped")
		}
	}
	if !strings.Contains(composed.Messages[1].Content, "live reply") {
		t.Fatalf("expected uncovered reply kept, got %+v", composed.Messages)
	}
}

func TestComposeContextWithArtifactsKeepsSegmentsEditedAfterCompletion(t *testing.T) {
	edited := textSegment("m1", 1000, "edited later")
	edited.EditedAtMs = 5000
	rc := RenderedContext{edited, textSegment("m2", 2000, "tail")}
	artifacts := []CompactionArtifact{{
		ID:             "a1",
		Summary:        "stale coverage",
		AnchorEndMs:    1000,
		CoverageAsOfMs: 4000,
		Sources:        []CompactionSource{{ExternalMessageID: "m1", CreatedAtMs: 1000}},
	}}

	// The edited segment is no longer what the summary replaced, so it stays
	// and the summary renders after it, at the anchor of what it covered.
	composed := ComposeContextWithArtifacts(rc, nil, artifacts)
	if composed == nil || len(composed.Messages) != 3 {
		t.Fatalf("expected summary and two independent sources, got %+v", composed)
	}
	if !strings.Contains(composed.Messages[0].Content, "edited later") {
		t.Fatalf("edited segment must survive coverage, got %+v", composed.Messages[0])
	}
	if composed.Messages[1].CompactionArtifactID != "a1" {
		t.Fatalf("expected summary after the edited segment, got %+v", composed.Messages[1])
	}
	if composed.Messages[2].Content != "tail" {
		t.Fatalf("current source must exclude edited history: %+v", composed.Messages[2])
	}
}

func TestComposeContextWithArtifactsDropsSegmentsEditedBeforeCompletion(t *testing.T) {
	edited := textSegment("m1", 1000, "edited early")
	edited.EditedAtMs = 3500
	rc := RenderedContext{edited, textSegment("m2", 2000, "tail")}
	artifacts := []CompactionArtifact{{
		ID:             "a1",
		Summary:        "covers the edit",
		AnchorEndMs:    1000,
		CoverageAsOfMs: 4000,
		Sources:        []CompactionSource{{ExternalMessageID: "m1", CreatedAtMs: 1000}},
	}}

	composed := ComposeContextWithArtifacts(rc, nil, artifacts)
	if composed == nil {
		t.Fatal("expected composed result")
	}
	for _, m := range composed.Messages {
		if strings.Contains(m.Content, "edited early") {
			t.Fatal("segment edited before completion must be covered")
		}
	}
}

func TestComposeContextWithArtifactsKeepsEditedSegmentWhenCompletionUnknown(t *testing.T) {
	edited := textSegment("m1", 1000, "edited unknown")
	edited.EditedAtMs = 1200
	rc := RenderedContext{edited}
	artifacts := []CompactionArtifact{{
		ID:      "a1",
		Summary: "no completion time",
		Sources: []CompactionSource{{ExternalMessageID: "m1", CreatedAtMs: 1000}},
	}}

	composed := ComposeContextWithArtifacts(rc, nil, artifacts)
	if composed == nil {
		t.Fatal("expected composed result")
	}
	found := false
	for _, m := range composed.Messages {
		if strings.Contains(m.Content, "edited unknown") {
			found = true
		}
	}
	if !found {
		t.Fatal("edited segment must survive when completion time is unknown")
	}
}

func TestComposeContextWithArtifactsIgnoresBlankSummary(t *testing.T) {
	rc := RenderedContext{textSegment("m1", 1000, "original")}
	artifacts := []CompactionArtifact{{
		ID:      "a1",
		Summary: "   \n ",
		Sources: []CompactionSource{{ExternalMessageID: "m1", CreatedAtMs: 1000}},
	}}

	composed := ComposeContextWithArtifacts(rc, nil, artifacts)
	if composed == nil || len(composed.Messages) != 1 {
		t.Fatalf("expected original message only, got %+v", composed)
	}
	if composed.Messages[0].CompactionArtifactID != "" || !strings.Contains(composed.Messages[0].Content, "original") {
		t.Fatalf("blank summary must not cover originals, got %+v", composed.Messages[0])
	}
}

func TestComposeContextWithArtifactsOrdersMultipleArtifacts(t *testing.T) {
	rc := RenderedContext{textSegment("m5", 5000, "current")}
	artifacts := []CompactionArtifact{
		{ID: "a2", Summary: "second window", AnchorEndMs: 3000},
		{ID: "a1", Summary: "first window", AnchorEndMs: 1000},
	}

	composed := ComposeContextWithArtifacts(rc, nil, artifacts)
	if composed == nil || len(composed.Messages) != 3 {
		t.Fatalf("expected 2 summaries + current, got %+v", composed)
	}
	if composed.Messages[0].CompactionArtifactID != "a1" || composed.Messages[1].CompactionArtifactID != "a2" {
		t.Fatalf("expected summaries ordered by anchor, got %v", messageTexts(composed.Messages))
	}
}

func TestComposeContextWithArtifactsEmptyInputs(t *testing.T) {
	if composed := ComposeContextWithArtifacts(nil, nil, nil); composed != nil {
		t.Fatalf("expected nil result for empty inputs, got %+v", composed)
	}
}

func TestActiveRenderedContextFiltersCoveredSegments(t *testing.T) {
	rc := RenderedContext{textSegment("m1", 1000, "covered"), textSegment("m2", 2000, "live")}
	artifacts := []CompactionArtifact{{
		ID:      "a1",
		Summary: "covers m1",
		Sources: []CompactionSource{{ExternalMessageID: "m1", CreatedAtMs: 1000}},
	}}

	active := ActiveRenderedContext(rc, artifacts)
	if len(active) != 1 || active[0].MessageID != "m2" {
		t.Fatalf("expected only live segment, got %+v", active)
	}
}

func TestComposeContextWithArtifactsKeepsSlotOrderWithinSameMs(t *testing.T) {
	rc := RenderedContext{
		textSegment("m1", 1000, "covered first"),
		textSegment("m2", 1000, "uncovered second"),
	}
	artifacts := []CompactionArtifact{{
		ID:          "a1",
		Summary:     "first slot",
		AnchorEndMs: 1000,
		Sources:     []CompactionSource{{ExternalMessageID: "m1", CreatedAtMs: 1000}},
	}}

	composed := ComposeContextWithArtifacts(rc, nil, artifacts)
	if composed == nil || len(composed.Messages) != 2 {
		t.Fatalf("expected summary + uncovered message, got %+v", composed)
	}
	if composed.Messages[0].CompactionArtifactID != "a1" {
		t.Fatalf("summary must keep the covered segment's slot, got %v", messageTexts(composed.Messages))
	}
	if !strings.Contains(composed.Messages[1].Content, "uncovered second") {
		t.Fatalf("uncovered same-ms message must follow the summary, got %v", messageTexts(composed.Messages))
	}
}

func TestComposeContextWithArtifactsPlacesSummaryOfAnEditedSlotAtItsAnchor(t *testing.T) {
	edited := textSegment("m2", 1000, "edited survivor")
	edited.EditedAtMs = 5000
	rc := RenderedContext{
		textSegment("m1", 1000, "uncovered first"),
		edited,
	}
	artifacts := []CompactionArtifact{{
		ID:             "a1",
		Summary:        "covers the edited slot",
		AnchorEndMs:    1000,
		CoverageAsOfMs: 4000,
		Sources:        []CompactionSource{{ExternalMessageID: "m2", CreatedAtMs: 1000}},
	}}

	composed := ComposeContextWithArtifacts(rc, nil, artifacts)
	if composed == nil || len(composed.Messages) != 3 {
		t.Fatalf("expected first + edited + summary, got %+v", composed)
	}
	if !strings.Contains(composed.Messages[0].Content, "uncovered first") {
		t.Fatalf("uncovered earlier slot must stay first, got %v", messageTexts(composed.Messages))
	}
	if !strings.Contains(composed.Messages[1].Content, "edited survivor") {
		t.Fatalf("the edited slot keeps its place, got %v", messageTexts(composed.Messages))
	}
	if composed.Messages[2].CompactionArtifactID != "a1" {
		t.Fatalf("summary of nothing it still replaces renders at its anchor, after that ms's segments, got %v", messageTexts(composed.Messages))
	}
}

func TestComposeContextWithArtifactsKeepsTurnResponseSlotOrderWithinSameMs(t *testing.T) {
	early := assistantTR(1000, "uncovered early reply")
	early.SourceMessageID = "h1"
	covered := assistantTR(1000, "covered later reply")
	covered.SourceMessageID = "h2"
	artifacts := []CompactionArtifact{{
		ID:          "a1",
		Summary:     "covers the later reply",
		AnchorEndMs: 1000,
		Sources:     []CompactionSource{{HistoryMessageID: "h2", CreatedAtMs: 1000}},
	}}

	composed := ComposeContextWithArtifacts(nil, []TurnResponseEntry{early, covered}, artifacts)
	if composed == nil || len(composed.Messages) != 2 {
		t.Fatalf("expected early reply + summary, got %+v", composed)
	}
	if !strings.Contains(composed.Messages[0].Content, "uncovered early reply") {
		t.Fatalf("uncovered earlier response must stay first, got %v", messageTexts(composed.Messages))
	}
	if composed.Messages[1].CompactionArtifactID != "a1" {
		t.Fatalf("summary must take the covered response slot, got %v", messageTexts(composed.Messages))
	}
}

// Two covered responses ahead of a survivor pin the response-side index space:
// keying summaries by filtered position would land them after the survivor.
func TestComposeContextWithArtifactsKeepsTurnResponseSlotAcrossMultipleCovered(t *testing.T) {
	first := assistantTR(1000, "covered first reply")
	first.SourceMessageID = "h1"
	second := assistantTR(1000, "covered second reply")
	second.SourceMessageID = "h2"
	survivor := assistantTR(1000, "surviving reply")
	survivor.SourceMessageID = "h3"
	artifacts := []CompactionArtifact{
		{
			ID:      "a1",
			Summary: "covers the first reply",
			Sources: []CompactionSource{{HistoryMessageID: "h1", CreatedAtMs: 1000}},
		},
		{
			ID:      "a2",
			Summary: "covers the second reply",
			Sources: []CompactionSource{{HistoryMessageID: "h2", CreatedAtMs: 1000}},
		},
	}

	composed := ComposeContextWithArtifacts(nil, []TurnResponseEntry{first, second, survivor}, artifacts)
	if composed == nil || len(composed.Messages) != 3 {
		t.Fatalf("expected two summaries + survivor, got %+v", composed)
	}
	if composed.Messages[0].CompactionArtifactID != "a1" || composed.Messages[1].CompactionArtifactID != "a2" {
		t.Fatalf("summaries must keep their covered slots in order, got %v", messageTexts(composed.Messages))
	}
	if !strings.Contains(composed.Messages[2].Content, "surviving reply") {
		t.Fatalf("survivor must follow both summaries, got %v", messageTexts(composed.Messages))
	}
}

// A source the summary does not cover that lies between sources it does, such
// as a passive message, renders before the summary instead of being dropped.
func TestComposeContextWithArtifactsRendersUncoveredSourcesInsideTheSpanBeforeTheSummary(t *testing.T) {
	reply := assistantTR(2500, "covered reply")
	reply.SourceMessageID = "h1"
	rc := RenderedContext{
		textSegment("m1", 1000, "covered question"),
		textSegment("m2", 2000, "passive chatter"),
		textSegment("m3", 3000, "current"),
	}
	artifacts := []CompactionArtifact{{
		ID:      "a1",
		Summary: "folded",
		Sources: []CompactionSource{{ExternalMessageID: "m1", CreatedAtMs: 1000}, {HistoryMessageID: "h1", CreatedAtMs: 2500}},
	}}

	composed := ComposeContextWithArtifacts(rc, []TurnResponseEntry{reply}, artifacts)
	got := messageTexts(composed.Messages)
	want := []string{"user:passive chatter", "user:<summary>\nfolded\n</summary>", "user:current"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("composed %q, want %q", got, want)
	}
}
