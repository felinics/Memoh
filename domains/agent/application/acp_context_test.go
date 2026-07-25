package application

import (
	"strings"
	"testing"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/engine"
)

func TestRenderACPContextMarkdownIncludesDynamicRuntimeAndMemory(t *testing.T) {
	t.Parallel()

	got := renderACPContextMarkdown(acpContextRenderInput{
		Now:                     time.Date(2026, 6, 1, 9, 30, 0, 0, time.FixedZone("PDT", -7*3600)),
		Timezone:                "America/Los_Angeles",
		BotID:                   "bot-1",
		SessionID:               "session-1",
		AgentID:                 "codex",
		ProjectPath:             "/data/app",
		DisplayName:             "Alice",
		CurrentChannel:          "telegram",
		ConversationType:        "group",
		ConversationName:        "Dev Group",
		SourceChannelIdentityID: "identity-1",
		Attachments: []agentdomain.Attachment{{
			Name: "spec.md",
			Path: "/data/uploads/spec.md",
			Mime: "text/markdown",
		}},
		Files: []engine.SystemFile{
			{Filename: "IDENTITY.md", Content: "I am Memo."},
			{Filename: "SOUL.md", Content: "Be concise."},
			{Filename: "TOOLS.md", Content: "Do not inject normal tool prompt."},
			{Filename: "MEMORY.md", Content: "User prefers small patches."},
			{Filename: "PROFILES.md", Content: "Alice is the project owner."},
			{Filename: "memory/preference/alice-profile.md", Content: "Alice prefers small, reviewable patches."},
		},
	})

	for _, want := range []string{
		"# Memoh ACP Context",
		"Current time: 2026-06-01T09:30:00-07:00",
		"Timezone: America/Los_Angeles",
		"Bot ID: bot-1",
		"ACP agent: codex",
		"Workspace: /data/app",
		"Sender: Alice",
		"Conversation name: Dev Group",
		"name=spec.md",
		"## Bot Identity",
		"Embedded excerpt from `/data/IDENTITY.md`",
		"I am Memo.",
		"## Bot Soul",
		"Be concise.",
		"## Long-Term Memory",
		"User prefers small patches.",
		"## Profiles",
		"Alice is the project owner.",
		"## Memory Concept - preference/alice-profile.md",
		"Alice prefers small, reviewable patches.",
		"This virtual resource is already embedded",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("context missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Do not inject normal tool prompt.") {
		t.Fatalf("TOOLS.md content should not be injected into ACP context:\n%s", got)
	}
}

func TestRenderACPContextMarkdownRespectsSystemFilesBudget(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("0123456789", 200) + "\nTAIL"
	got := renderACPContextMarkdown(acpContextRenderInput{
		Now:                 time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC),
		Timezone:            "UTC",
		BotID:               "bot-1",
		SessionID:           "session-1",
		AgentID:             "codex",
		ProjectPath:         "/data/app",
		SystemFilesMaxBytes: 512,
		Files: []engine.SystemFile{
			{Filename: "MEMORY.md", Content: large},
			{Filename: "PROFILES.md", Content: "SECOND_FILE_SHOULD_NOT_FIT"},
		},
	})

	if !strings.Contains(got, "[memoh pruned]") {
		t.Fatalf("context missing prune marker:\n%s", got)
	}
	if strings.Contains(got, "SECOND_FILE_SHOULD_NOT_FIT") {
		t.Fatalf("context included system file content beyond budget:\n%s", got)
	}
}
