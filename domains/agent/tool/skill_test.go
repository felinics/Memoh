package tool

import (
	"context"
	"testing"
)

func TestListSkillReturnsSortedSummaries(t *testing.T) {
	provider := NewSkillProvider(nil)

	toolset, err := provider.Tools(context.Background(), SessionContext{
		Skills: map[string]SkillDetail{
			"zeta": {
				Description: "Zeta instructions",
				Content:     "Do not include this full content.",
				Path:        "/data/skills/zeta",
			},
			"alpha": {
				Description: "Alpha instructions",
				Content:     "Do not include this full content either.",
				Path:        "/data/skills/alpha",
			},
		},
	})
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	if len(toolset) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(toolset))
	}
	if got := toolset[0].Name; got != "list_skills" {
		t.Fatalf("first tool = %q, want list_skills", got)
	}

	result, err := toolset[0].Execute(nil, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", result)
	}
	if got := payload["count"]; got != 2 {
		t.Fatalf("count = %#v, want 2", got)
	}
	items, ok := payload["skills"].([]map[string]any)
	if !ok {
		t.Fatalf("skills type = %T, want []map[string]any", payload["skills"])
	}
	if got := items[0]["name"]; got != "alpha" {
		t.Fatalf("first skill name = %#v, want alpha", got)
	}
	if got := items[1]["name"]; got != "zeta" {
		t.Fatalf("second skill name = %#v, want zeta", got)
	}
	if _, ok := items[0]["content"]; ok {
		t.Fatalf("list_skills should not return full skill content")
	}
}

func TestUseSkillReturnsPath(t *testing.T) {
	provider := NewSkillProvider(nil)

	toolset, err := provider.Tools(context.Background(), SessionContext{
		Skills: map[string]SkillDetail{
			"pdf": {
				Description: "Read PDF instructions",
				Content:     "Use a PDF-aware workflow.",
				Path:        "/data/.agents/skills/pdf",
			},
		},
	})
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	if len(toolset) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(toolset))
	}

	result, err := toolset[1].Execute(nil, map[string]any{
		"skillName": "pdf",
		"reason":    "Need to process a PDF attachment",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", result)
	}
	if got := payload["path"]; got != "/data/.agents/skills/pdf" {
		t.Fatalf("path = %#v, want %q", got, "/data/.agents/skills/pdf")
	}
}
