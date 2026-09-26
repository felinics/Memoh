package timeline

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
)

func TestComposeBudgetedPreservesSourceBoundariesAndFitsReadmission(t *testing.T) {
	rc := make(RenderedContext, 1000)
	for i := range rc {
		rc[i] = textSegment(fmt.Sprintf("m%d", i), int64(i+1), strings.Repeat("x", 400))
	}
	artifacts := []CompactionArtifact{{ID: "summary", Summary: strings.Repeat("s", 400000-len("<summary>\n\n</summary>"))}}
	composed, admission := ComposeContextWithArtifactsBudgeted(rc, nil, artifacts, ComposeBudget{MaxTokens: 200000})
	if composed == nil || admission.ProtectedOverflow {
		t.Fatalf("source messages should fit: %+v", admission)
	}
	entries := make([]turn.AdmissionEntry, len(composed.Messages))
	for i, message := range composed.Messages {
		entries[i] = turn.AdmissionEntry{Cost: turn.EstimateTokensFromBytes(len(message.Content)), Pinned: message.CompactionArtifactID != ""}
	}
	readmission := turn.AdmitContextEntries(entries, 200000)
	if readmission.ProtectedOverflow || readmission.DroppedEntries != 0 {
		t.Errorf("materialized messages must remain admissible: %+v", readmission)
	}
	if composed.EstimatedTokens != readmission.EstimatedTokens || admission.SelectedTokens != readmission.EstimatedTokens {
		t.Errorf("composition=%d selected=%d materialized=%d", composed.EstimatedTokens, admission.SelectedTokens, readmission.EstimatedTokens)
	}
	if got := composed.Messages[len(composed.Messages)-1].Content; got != rc[len(rc)-1].Content[0].Text {
		t.Errorf("current source includes history: got %d bytes, want 400", len(got))
	}
}

func TestComposeBudgetedIncludesSourceInternalSeparators(t *testing.T) {
	rc := RenderedContext{{Content: []RenderedContentPiece{{Type: "text", Text: "xxxx"}, {Type: "text", Text: "yyy"}}}}
	composed, admission := ComposeContextWithArtifactsBudgeted(rc, nil, nil, ComposeBudget{MaxTokens: 1})
	if composed != nil || !admission.ProtectedOverflow {
		t.Fatalf("eight rendered bytes cannot fit one token: composed=%+v admission=%+v", composed, admission)
	}
}

func FuzzComposeBudgetedMatchesRenderedCost(f *testing.F) {
	f.Add([]byte("abc\n群聊hello"), uint16(20))
	f.Add([]byte{0, 1, 3, 4, 0, 15, 7, 255}, uint16(1))
	f.Fuzz(func(t *testing.T, input []byte, limit uint16) {
		if len(input) > 4096 {
			return
		}
		rc := make(RenderedContext, 0, len(input))
		for i, n := range input {
			rc = append(rc, RenderedSegment{ReceivedAtMs: int64(i + 1), Content: []RenderedContentPiece{
				{Type: "text", Text: strings.Repeat("a", int(n)%17)},
				{Type: "image", Text: "not text"},
				{Type: "text", Text: strings.Repeat("群", int(n)%7)},
				{Type: "text", Text: ""},
			}})
		}
		budget := int(limit) + 1
		composed, admission := ComposeContextWithArtifactsBudgeted(rc, nil, nil, ComposeBudget{MaxTokens: budget})
		if composed == nil {
			return
		}
		total := 0
		for _, message := range composed.Messages {
			total += turn.EstimateTokensFromBytes(len(message.Content))
		}
		if total > budget || total != composed.EstimatedTokens || total != admission.SelectedTokens {
			t.Fatalf("rendered=%d budget=%d composed=%d admission=%+v", total, budget, composed.EstimatedTokens, admission)
		}
	})
}

func BenchmarkComposeSourceMessages(b *testing.B) {
	for _, size := range []int{200, 1000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			rc := make(RenderedContext, size)
			for i := range rc {
				rc[i] = textSegment(strconv.Itoa(i), int64(i+1), strings.Repeat("x", 400))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				ComposeContextWithArtifactsBudgeted(rc, nil, nil, ComposeBudget{MaxTokens: 200000})
			}
		})
	}
}
