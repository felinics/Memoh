package tools

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

func TestMessageSnapshotIsImmutableAndProviderNeutral(t *testing.T) {
	original := []sdk.Message{{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{
			sdk.TextPart{
				Text:             "answer",
				CacheControl:     &sdk.CacheControl{Type: "ephemeral"},
				ProviderMetadata: partmeta.Fold(map[string]any{"signature": "provider-only"}),
			},
			sdk.ToolCallPart{
				ToolCallID:       "call-1",
				ToolName:         "read",
				Input:            toolexec.ArgumentsFromValue(map[string]any{"path": "a.txt"}),
				ProviderMetadata: partmeta.Fold(map[string]any{"opaque": "value"}),
			},
		},
	}}
	snapshot := NewMessageSnapshot(original)
	original[0].Content[0] = sdk.TextPart{Text: "mutated"}

	messages, err := snapshot.Messages()
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	text := messages[0].Content[0].(sdk.TextPart)
	if text.Text != "answer" || text.CacheControl != nil || len(text.ProviderMetadata) != 0 {
		t.Fatalf("unexpected provider-neutral text part: %+v", text)
	}
	call := messages[0].Content[1].(sdk.ToolCallPart)
	if call.ToolCallID != "call-1" || len(call.ProviderMetadata) != 0 {
		t.Fatalf("unexpected provider-neutral tool call: %+v", call)
	}
}

func TestMessageSnapshotRetainsSourcesOnlyForUnchangedMessages(t *testing.T) {
	first := sdk.UserMessage("first")
	second := sdk.AssistantMessage("second")
	snapshot := NewMessageSnapshotWithSources(
		[]sdk.Message{first, second},
		[]string{"message-1", "message-2"},
	)

	secondWithProviderState := second
	secondWithProviderState.Content[0] = sdk.TextPart{
		Text:         "second",
		CacheControl: &sdk.CacheControl{Type: "ephemeral"},
	}
	snapshot.Store([]sdk.Message{
		sdk.SystemMessage("runtime-only"),
		secondWithProviderState,
		sdk.UserMessage("new tool delta"),
	})

	entries, err := snapshot.Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].SourceMessageID != "" || entries[1].SourceMessageID != "message-2" || entries[2].SourceMessageID != "" {
		t.Fatalf("unexpected retained sources: %+v", entries)
	}

	snapshot.Store([]sdk.Message{sdk.AssistantMessage("second changed")})
	entries, err = snapshot.Entries()
	if err != nil {
		t.Fatalf("Entries after change: %v", err)
	}
	if len(entries) != 1 || entries[0].SourceMessageID != "" {
		t.Fatalf("changed message retained a source: %+v", entries)
	}
}

// TestMessageSnapshotSourcesFollowProviderNeutralJSON pins the source rule
// against its specification: each new message takes the source of the earliest
// old message after the previous match whose provider-neutral JSON is equal.
func TestMessageSnapshotSourcesFollowProviderNeutralJSON(t *testing.T) {
	user := sdk.UserMessage("question")
	call := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
		sdk.TextPart{Text: "reading"},
		sdk.ToolCallPart{ToolCallID: "call-1", ToolName: "read", Input: toolexec.ArgumentsFromValue(map[string]any{"path": "a.txt"})},
	}}
	result := sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
		sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "read", Result: toolexec.OutputFromValue(map[string]any{"content": "<a & b>"})},
	}}
	cachedResult := sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
		sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "read", Result: result.Content[0].(sdk.ToolResultPart).Result, CacheControl: &sdk.CacheControl{Type: "ephemeral"}},
	}}
	answer := sdk.AssistantMessage("done")
	ok := sdk.UserMessage("ok")
	summary := sdk.UserMessage("summary of earlier turns")
	providerState := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
		&sdk.TextPart{Text: "reading", CacheControl: &sdk.CacheControl{Type: "ephemeral"}},
		sdk.ToolCallPart{ToolCallID: "call-1", ToolName: "read", Input: toolexec.ArgumentsFromValue(map[string]any{"path": "a.txt"}), ProviderMetadata: sdk.NewProviderMetadata("p", map[string]string{"k": "v"})},
	}}
	looseArguments := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
		sdk.TextPart{Text: "reading"},
		sdk.ToolCallPart{ToolCallID: "call-1", ToolName: "read", Input: sdk.ToolArguments{JSON: []byte(`{ "path" : "a.txt" }`)}},
	}}
	copiedResult := sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
		sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "read", Result: sdk.ToolOutput{JSON: append([]byte(nil), result.Content[0].(sdk.ToolResultPart).Result.JSON...)}},
	}}
	seed := []sdk.Message{user, call, result, ok, answer, ok}
	seedSources := []string{"m-user", "m-call", "m-result", "m-ok-1", "m-answer", "m-ok-2"}

	cases := []struct {
		name     string
		attempts [][]sdk.Message
	}{
		{name: "extension and retry", attempts: [][]sdk.Message{
			append(append([]sdk.Message(nil), seed...), call),
			append(append([]sdk.Message(nil), seed...), call),
			append(append([]sdk.Message(nil), seed...), call, result),
		}},
		{name: "fold replaces the prefix", attempts: [][]sdk.Message{
			{summary, ok, answer, ok},
			{summary, ok, answer, ok, user},
		}},
		{name: "runtime message prepended then dropped", attempts: [][]sdk.Message{
			append([]sdk.Message{sdk.SystemMessage("runtime-only")}, seed...),
			seed,
		}},
		{name: "cache breakpoint moves across tool results", attempts: [][]sdk.Message{
			{user, call, cachedResult},
			{user, call, result, call, cachedResult},
		}},
		{name: "provider state and pointer parts are neutral", attempts: [][]sdk.Message{
			{user, providerState, result, ok},
		}},
		{name: "equal JSON in different bytes", attempts: [][]sdk.Message{
			{user, looseArguments, copiedResult, ok},
		}},
		{name: "duplicates removed and reordered", attempts: [][]sdk.Message{
			{ok, user, ok, answer},
			{answer, ok, user},
			{},
			{ok},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := NewMessageSnapshotWithSources(seed, seedSources)
			wantMessages, wantSources := seed, seedSources
			for i, attempt := range tc.attempts {
				snapshot.Store(attempt)
				wantSources = specSnapshotSources(t, wantMessages, wantSources, attempt)
				wantMessages = attempt
				assertSnapshotEntries(t, snapshot, wantMessages, wantSources, fmt.Sprintf("attempt %d", i))
			}
		})
	}
}

// TestMessageSnapshotSourcesMatchSpecOnRandomEdits drives random sequences of
// appends, prefix rewrites, removals, and swaps over a small alphabet so equal
// messages recur, comparing every attempt with the specification.
func TestMessageSnapshotSourcesMatchSpecOnRandomEdits(t *testing.T) {
	alphabet := []sdk.Message{
		sdk.UserMessage("a"),
		sdk.UserMessage("b"),
		sdk.AssistantMessage("a"),
		{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{sdk.ToolResultPart{ToolCallID: "c", ToolName: "t", Result: sdk.TextOutput("r")}}},
		{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{sdk.ToolResultPart{ToolCallID: "c", ToolName: "t", Result: sdk.TextOutput("r"), CacheControl: &sdk.CacheControl{Type: "ephemeral"}}}},
	}
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fixed seed keeps the edit sequences reproducible
	for run := range 200 {
		messages := make([]sdk.Message, 0, 8)
		sources := make([]string, 0, 8)
		for i := range rng.IntN(8) {
			messages = append(messages, alphabet[rng.IntN(len(alphabet))])
			sources = append(sources, fmt.Sprintf("m-%d-%d", run, i))
		}
		snapshot := NewMessageSnapshotWithSources(messages, sources)
		for step := range 6 {
			next := append([]sdk.Message(nil), messages...)
			switch rng.IntN(4) {
			case 0:
				next = append(next, alphabet[rng.IntN(len(alphabet))])
			case 1:
				if len(next) > 0 {
					next[rng.IntN(len(next))] = alphabet[rng.IntN(len(alphabet))]
				}
			case 2:
				if len(next) > 0 {
					i := rng.IntN(len(next))
					next = append(next[:i], next[i+1:]...)
				}
			case 3:
				if len(next) > 1 {
					i, j := rng.IntN(len(next)), rng.IntN(len(next))
					next[i], next[j] = next[j], next[i]
				}
			}
			snapshot.Store(next)
			sources = specSnapshotSources(t, messages, sources, next)
			messages = next
			assertSnapshotEntries(t, snapshot, messages, sources, fmt.Sprintf("run %d step %d", run, step))
		}
	}
}

func specSnapshotSources(t *testing.T, old []sdk.Message, oldSources []string, messages []sdk.Message) []string {
	t.Helper()
	key := func(message sdk.Message) string {
		raw, err := json.Marshal(providerNeutralMessages([]sdk.Message{message})[0])
		if err != nil {
			t.Fatalf("marshal message: %v", err)
		}
		return string(raw)
	}
	sources := make([]string, len(messages))
	next := 0
	for i, message := range messages {
		for j := next; j < len(old); j++ {
			if key(old[j]) == key(message) {
				sources[i] = oldSources[j]
				next = j + 1
				break
			}
		}
	}
	return sources
}

func assertSnapshotEntries(t *testing.T, snapshot *MessageSnapshot, messages []sdk.Message, sources []string, label string) {
	t.Helper()
	entries, err := snapshot.Entries()
	if err != nil {
		t.Fatalf("%s: Entries: %v", label, err)
	}
	if len(entries) != len(messages) {
		t.Fatalf("%s: entries = %d, want %d", label, len(entries), len(messages))
	}
	for i, entry := range entries {
		if entry.SourceMessageID != sources[i] {
			t.Fatalf("%s: entry %d source = %q, want %q", label, i, entry.SourceMessageID, sources[i])
		}
		got, err := json.Marshal(entry.Message)
		if err != nil {
			t.Fatalf("%s: marshal entry %d: %v", label, i, err)
		}
		want, err := json.Marshal(providerNeutralMessages(messages[i : i+1])[0])
		if err != nil {
			t.Fatalf("%s: marshal message %d: %v", label, i, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s: entry %d = %s, want %s", label, i, got, want)
		}
	}
}

// BenchmarkMessageSnapshotStoreAttempt measures one provider attempt
// publishing an 8 MB, 520-message request whose prefix the previous attempt
// already stored.
func BenchmarkMessageSnapshotStoreAttempt(b *testing.B) {
	history, sources := largeSnapshotHistory(260, 30<<10)
	snapshot := NewMessageSnapshotWithSources(history, sources)
	attempt := append(append([]sdk.Message(nil), history...),
		sdk.AssistantMessage("next step"),
	)
	snapshot.Store(attempt)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		snapshot.Store(attempt)
	}
}

func largeSnapshotHistory(rounds, outputBytes int) ([]sdk.Message, []string) {
	payload := strings.Repeat("x", outputBytes)
	messages := make([]sdk.Message, 0, rounds*2)
	sources := make([]string, 0, rounds*2)
	for i := range rounds {
		callID := fmt.Sprintf("call-%d", i)
		messages = append(messages,
			sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
				sdk.TextPart{Text: fmt.Sprintf("step %d", i)},
				sdk.ToolCallPart{ToolCallID: callID, ToolName: "read", Input: toolexec.ArgumentsFromValue(map[string]any{"path": fmt.Sprintf("file-%d.txt", i)})},
			}},
			sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
				sdk.ToolResultPart{ToolCallID: callID, ToolName: "read", Result: toolexec.OutputFromValue(map[string]any{"content": payload, "line": i})},
			}},
		)
		sources = append(sources, fmt.Sprintf("assistant-%d", i), fmt.Sprintf("tool-%d", i))
	}
	return messages, sources
}
