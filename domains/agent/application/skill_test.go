package application

import "testing"

func TestNormalizeGatewaySkillPreservesPath(t *testing.T) {
	got, ok := normalizeGatewaySkill(SkillEntry{
		Name:        "pdf",
		Description: "Read PDF instructions",
		Content:     "Use a PDF-aware workflow.",
		Path:        " /data/.agents/skills/pdf ",
	})
	if !ok {
		t.Fatal("normalizeGatewaySkill returned ok=false")
	}
	if got.Path != "/data/.agents/skills/pdf" {
		t.Fatalf("path = %q, want %q", got.Path, "/data/.agents/skills/pdf")
	}
}
