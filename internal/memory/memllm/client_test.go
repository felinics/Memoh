package memllm

import (
	"strings"
	"testing"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
)

func TestParseJSONStringArray_Valid(t *testing.T) {
	t.Parallel()
	result := parseJSONStringArray(`["fact one", "fact two"]`)
	if len(result) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(result))
	}
	if result[0] != "fact one" || result[1] != "fact two" {
		t.Fatalf("unexpected facts: %v", result)
	}
}

func TestParseJSONStringArray_Empty(t *testing.T) {
	t.Parallel()
	result := parseJSONStringArray(`[]`)
	if len(result) != 0 {
		t.Fatalf("expected 0 facts, got %d", len(result))
	}
}

func TestParseJSONStringArray_CodeFence(t *testing.T) {
	t.Parallel()
	input := "```json\n[\"hello\", \"world\"]\n```"
	result := parseJSONStringArray(input)
	if len(result) != 2 {
		t.Fatalf("expected 2 facts from code fence, got %d", len(result))
	}
}

func TestParseJSONStringArray_PrefixText(t *testing.T) {
	t.Parallel()
	input := "Here are the facts:\n[\"a\", \"b\"]"
	result := parseJSONStringArray(input)
	if len(result) != 2 {
		t.Fatalf("expected 2 facts with prefix text, got %d", len(result))
	}
}

func TestParseJSONStringArray_Garbage(t *testing.T) {
	t.Parallel()
	result := parseJSONStringArray("this is not json at all")
	if result != nil {
		t.Fatalf("expected nil for garbage input, got %v", result)
	}
}

func TestParseJSONStringArray_FiltersBlanks(t *testing.T) {
	t.Parallel()
	result := parseJSONStringArray(`["fact one", "", "  ", "fact two"]`)
	if len(result) != 2 {
		t.Fatalf("expected 2 non-empty facts, got %d", len(result))
	}
}

func TestCompactSystemPromptDefinesLongTermMemoryCompactionContract(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"target_count",
		"Cluster related memories",
		"Preserve safety-critical",
		"Resolve conflicts",
		"Drop duplicates",
		"JSON array only",
	} {
		if !strings.Contains(compactSystemPrompt, want) {
			t.Fatalf("compact prompt missing %q:\n%s", want, compactSystemPrompt)
		}
	}
}

func TestParseExtractResponse_FactsWrapper(t *testing.T) {
	t.Parallel()
	input := `{"facts": ["Name is John", "Is a Software engineer"]}`
	result := parseExtractResponse(input)
	if len(result) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(result))
	}
	if result[0] != "Name is John" {
		t.Fatalf("unexpected first fact: %q", result[0])
	}
}

func TestParseExtractResponse_BareArray(t *testing.T) {
	t.Parallel()
	input := `["fact one", "fact two"]`
	result := parseExtractResponse(input)
	if len(result) != 2 {
		t.Fatalf("expected 2 facts from bare array, got %d", len(result))
	}
}

func TestParseExtractResponse_EmptyFacts(t *testing.T) {
	t.Parallel()
	result := parseExtractResponse(`{"facts": []}`)
	if len(result) != 0 {
		t.Fatalf("expected 0 facts, got %d", len(result))
	}
}

func TestParseExtractResponseWithSources(t *testing.T) {
	t.Parallel()
	result := parseExtractResponseWithSources(`{"facts":[
		{"text":"Likes tea","message_indices":[0,2]},
		{"text":"Lives in Berlin","message_indices":[1]}
	]}`)
	if len(result) != 2 || result[0].Text != "Likes tea" {
		t.Fatalf("unexpected extracted facts: %+v", result)
	}
	if len(result[0].MessageIndices) != 2 || result[0].MessageIndices[1] != 2 {
		t.Fatalf("unexpected source indices: %+v", result[0].MessageIndices)
	}
}

func TestParseUpdateResponse_Mem0Format(t *testing.T) {
	t.Parallel()
	input := `{"memory": [
		{"id": "0", "text": "User is a software engineer", "event": "NONE"},
		{"id": "1", "text": "Name is John", "event": "ADD", "source_fact_indices":[0]},
		{"id": "2", "text": "Loves cheese pizza", "event": "DELETE"},
		{"id": "3", "text": "Moved to Berlin", "event": "UPDATE", "old_memory": "Lives in Tokyo"}
	]}`
	result := parseUpdateResponse(input)
	if len(result) != 4 {
		t.Fatalf("expected 4 actions, got %d", len(result))
	}
	if result[0].Event != "NOOP" {
		t.Fatalf("expected NONE mapped to NOOP, got %q", result[0].Event)
	}
	if result[1].Event != "ADD" || result[1].Text != "Name is John" {
		t.Fatalf("unexpected ADD action: %+v", result[1])
	}
	if len(result[1].SourceFactIndices) != 1 || result[1].SourceFactIndices[0] != 0 {
		t.Fatalf("unexpected ADD provenance: %+v", result[1].SourceFactIndices)
	}
	if result[2].Event != "DELETE" || result[2].ID != "2" {
		t.Fatalf("unexpected DELETE action: %+v", result[2])
	}
	if result[3].Event != "UPDATE" || result[3].OldMemory != "Lives in Tokyo" {
		t.Fatalf("unexpected UPDATE action: %+v", result[3])
	}
}

func TestParseUpdateResponse_FlatArrayFallback(t *testing.T) {
	t.Parallel()
	input := `[{"event":"ADD","text":"User likes tea"},{"event":"NOOP"},{"event":"DELETE","id":"bot-1:mem_123"}]`
	result := parseUpdateResponse(input)
	if len(result) != 3 {
		t.Fatalf("expected 3 actions, got %d", len(result))
	}
}

func TestParseUpdateResponse_WithCodeFence(t *testing.T) {
	t.Parallel()
	input := "```json\n{\"memory\": [{\"event\":\"ADD\",\"text\":\"foo\",\"id\":\"1\"}]}\n```"
	result := parseUpdateResponse(input)
	if len(result) != 1 {
		t.Fatalf("expected 1 action from code fence, got %d", len(result))
	}
}

func TestParseUpdateResponse_EmptyMemory(t *testing.T) {
	t.Parallel()
	result := parseUpdateResponse(`{"memory": []}`)
	if result != nil {
		t.Fatalf("expected nil for empty memory array, got %v", result)
	}
}

func TestParseUpdateResponse_Garbage(t *testing.T) {
	t.Parallel()
	result := parseUpdateResponse("not json")
	if result != nil {
		t.Fatalf("expected nil for garbage, got %v", result)
	}
}

func TestExtractJSONBlock_NoFence(t *testing.T) {
	t.Parallel()
	got := extractJSONBlock(`["a"]`)
	if got != `["a"]` {
		t.Fatalf("expected raw pass-through, got %q", got)
	}
}

func TestExtractJSONBlock_JSONFence(t *testing.T) {
	t.Parallel()
	got := extractJSONBlock("```json\n[\"a\"]\n```")
	if got != `["a"]` {
		t.Fatalf("expected extracted content, got %q", got)
	}
}

func TestExtractJSONBlock_PlainFence(t *testing.T) {
	t.Parallel()
	got := extractJSONBlock("```\n[\"a\"]\n```")
	if got != `["a"]` {
		t.Fatalf("expected extracted content, got %q", got)
	}
}

var _ adapters.LLM = (*Client)(nil)
