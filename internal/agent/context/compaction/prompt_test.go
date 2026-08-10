package compaction

import (
	"strings"
	"testing"
)

func TestNonFusionPromptRemainsByteIdentical(t *testing.T) {
	t.Parallel()

	wantSystem := `You are a conversation summarizer. Given a conversation history, produce a concise summary that preserves:
- Key facts, decisions, and agreements
- User preferences and requests
- Important context needed for continuing the conversation
- Names, dates, numbers, and specific details
- Tool usage outcomes and their results

If <prior_context> is provided, it contains summaries of earlier conversation segments. Use them ONLY to understand the conversation flow and maintain continuity. Do NOT include, repeat, or rephrase any content from <prior_context> in your output.

For tool results, only include key outcomes; ignore intermediate steps or errors.

Output ONLY the summary of the new conversation segment. No preamble, no headers.`
	if systemPrompt != wantSystem {
		t.Fatalf("non-fusion system prompt changed:\n%s", systemPrompt)
	}

	wantUser := "<prior_context>\n" +
		"The following are summaries of earlier parts of this conversation. They are provided ONLY as reference context to help you understand the conversation flow. Do NOT include or repeat any of this content in your output summary.\n\n" +
		"first summary\n---\nsecond summary\n" +
		"</prior_context>\n\n" +
		"Now summarize the following conversation segment:\n" +
		"user: new question\nassistant: new answer\n"
	gotUser := buildUserPrompt(
		[]string{"first summary", "second summary"},
		[]messageEntry{{Role: "user", Content: "new question"}, {Role: "assistant", Content: "new answer"}},
	)
	if gotUser != wantUser {
		t.Fatalf("non-fusion user prompt changed:\n%s", gotUser)
	}
}

func TestFusionPromptRendersAbsorbedContextWithoutPriorContext(t *testing.T) {
	t.Parallel()

	absorbed := []absorbedSegment{
		{Source: absorbedSourceRawTranscript, Content: "user: canonical raw"},
		{Source: absorbedSourceEarlierSummary, Content: "earlier condensed state"},
	}
	prompt := buildFusionUserPrompt(
		nil,
		absorbed,
		[]messageEntry{{Role: "user", Content: "new conversation"}},
	)

	for _, want := range []string{
		"<absorbed_context>",
		"[raw transcript segment]",
		"[earlier summary segment]",
		"user: canonical raw",
		"earlier condensed state",
		"user: new conversation\n",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("fusion user prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "<prior_context>") {
		t.Fatalf("whole-frontier fusion prompt retained prior context:\n%s", prompt)
	}
	for _, want := range []string{
		"REPLACES",
		"Integrate all still-relevant information",
		"see prior summary",
	} {
		if !strings.Contains(fusionSystemPrompt, want) {
			t.Fatalf("fusion system prompt missing %q:\n%s", want, fusionSystemPrompt)
		}
	}
}

func TestCapPriorSummariesKeepsNewestWithinBudget(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", 400) // ~100 tokens each
	summaries := []string{"oldest " + big, "middle " + big, "newest " + big}

	capped := capPriorSummaries(summaries, 220)
	if len(capped) != 2 {
		t.Fatalf("capped = %d summaries, want 2", len(capped))
	}
	if !strings.HasPrefix(capped[0], "middle") || !strings.HasPrefix(capped[1], "newest") {
		t.Fatalf("cap must keep the newest summaries in chronological order, got %q", capped)
	}
}

func TestCapPriorSummariesAlwaysKeepsTheNewestTruncatedToBudget(t *testing.T) {
	t.Parallel()

	summaries := []string{"old", strings.Repeat("y", 4000)}
	capped := capPriorSummaries(summaries, 10)
	if len(capped) != 1 || !strings.HasPrefix(capped[0], "y") {
		t.Fatalf("cap must keep at least the newest summary, got %q", capped)
	}
	if got := estimateBytesAsTokens(capped[0]) + priorSeparatorTokens; got > 10 {
		t.Fatalf("the cap is a hard bound, marker included: got ~%d tokens for cap 10", got)
	}
}

func TestCapPriorSummariesDropsAllOnNonPositiveBudget(t *testing.T) {
	t.Parallel()

	if got := capPriorSummaries([]string{"a", "b"}, 0); got != nil {
		t.Fatalf("non-positive budget must drop all prior context, got %q", got)
	}
	if got := capPriorSummaries([]string{"a", "b"}, -5); got != nil {
		t.Fatalf("negative budget must drop all prior context, got %q", got)
	}
}

func TestBuildUserPromptOrdersPriorContextChronologically(t *testing.T) {
	t.Parallel()

	prompt := buildUserPrompt([]string{"first segment", "second segment"}, []messageEntry{{Role: "user", Content: "now"}})
	if !strings.Contains(prompt, "first segment\n---\nsecond segment") {
		t.Fatalf("prior context must stay in chronological order:\n%s", prompt)
	}
}

func TestCapEntriesToBudgetHoldsTheTotalAcrossManyEntries(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		entries []messageEntry
		max     int
	}{
		{"two oversized", []messageEntry{
			{Role: "user", Content: strings.Repeat("a", 2400)},
			{Role: "assistant", Content: strings.Repeat("b", 2400)},
		}, 1000},
		{"one giant many small", []messageEntry{
			{Role: "user", Content: strings.Repeat("a", 4800)},
			{Role: "assistant", Content: strings.Repeat("b", 40)},
			{Role: "user", Content: strings.Repeat("c", 40)},
			{Role: "assistant", Content: strings.Repeat("d", 40)},
		}, 1000},
		{"tight budget", []messageEntry{
			{Role: "user", Content: strings.Repeat("a", 400)},
			{Role: "tool", Content: strings.Repeat("b", 400)},
			{Role: "assistant", Content: strings.Repeat("c", 400)},
		}, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			capped := capEntriesToBudget(tc.entries, tc.max)
			if len(capped) != len(tc.entries) {
				t.Fatalf("entries dropped: got %d, want %d (ids must stay aligned)", len(capped), len(tc.entries))
			}
			total := 0
			for _, e := range capped {
				total += estimateBytesAsTokens(e.Content) + estimateBytesAsTokens(e.Role) + 1
			}
			if total > tc.max {
				t.Fatalf("capped total = %d tokens, exceeds max = %d", total, tc.max)
			}
		})
	}
}

// TestCapEntriesToBudgetFloorsCanExceedBudget pins the documented limit of
// the cap: entries are never dropped, so when the per-entry floors alone
// exceed maxTokens the capped total still does. entriesPromptCost is the
// recheck callers use to detect that overshoot.
func TestCapEntriesToBudgetFloorsCanExceedBudget(t *testing.T) {
	t.Parallel()

	entries := make([]messageEntry, 100)
	for i := range entries {
		entries[i] = messageEntry{Role: "tool", Content: "[tool result]"}
	}
	capped := capEntriesToBudget(entries, 50)
	if len(capped) != len(entries) {
		t.Fatalf("entries dropped: got %d, want %d (ids must stay aligned)", len(capped), len(entries))
	}
	if got := entriesPromptCost(capped); got <= 50 {
		t.Fatalf("floors of 100 minimal entries cannot fit 50 tokens, got %d — update the cap's documentation if this now holds", got)
	}
}

func TestCapEntriesToBudgetKeepsCheapShortEntries(t *testing.T) {
	t.Parallel()

	entries := []messageEntry{{Role: "user", Content: strings.Repeat("g", 4800)}}
	for i := 0; i < 20; i++ {
		entries = append(entries, messageEntry{Role: "tool", Content: "a"})
	}
	capped := capEntriesToBudget(entries, 100)
	total := 0
	for i, e := range capped {
		total += estimateBytesAsTokens(e.Content) + estimateBytesAsTokens(e.Role) + 1
		if i > 0 && e.Content != "a" {
			t.Fatalf("short entry %d was rewritten to %q: a marker must never replace cheaper original text", i, e.Content)
		}
	}
	if total > 100 {
		t.Fatalf("capped total = %d tokens, exceeds max = 100", total)
	}
}
