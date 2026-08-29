package native

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/sessionmode"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func TestGenerateSystemPromptIncludesPlatformIdentitiesInChat(t *testing.T) {
	t.Parallel()

	prompt := GenerateSystemPrompt(SystemPromptParams{
		SessionType:               sessionmode.Chat,
		Timezone:                  "UTC",
		PlatformIdentitiesSection: "## Platform Identities\n\n<identity channel=\"telegram\" username=\"@memoh\"/>",
	})

	if !strings.Contains(prompt, "## Platform Identities") {
		t.Fatalf("expected platform identities heading in prompt")
	}
	if !strings.Contains(prompt, `<identity channel="telegram" username="@memoh"/>`) {
		t.Fatalf("expected platform identity XML in prompt")
	}
}

func TestGenerateSystemPromptOmitsVolatileCurrentTime(t *testing.T) {
	t.Parallel()

	prompt := GenerateSystemPrompt(SystemPromptParams{
		SessionType: sessionmode.Chat,
		Timezone:    "Asia/Singapore",
	})
	if strings.Contains(prompt, "Current time:") || strings.Contains(prompt, "{{currentTime}}") {
		t.Fatalf("system prompt contains a volatile current time:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Timezone: Asia/Singapore") {
		t.Fatalf("system prompt must retain the stable timezone:\n%s", prompt)
	}
}

func TestGenerateSystemPromptIncludesCommonAndModeContracts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		sessionType string
		want        []string
	}{
		{
			sessionType: sessionmode.Chat,
			want: []string{
				"You are an AI agent running inside a private Memoh workspace.",
				"## Session mode: chat",
				"Your text output is sent directly to the current conversation.",
			},
		},
		{
			sessionType: sessionmode.Discuss,
			want: []string{
				"You are an AI agent running inside a private Memoh workspace.",
				"## Session mode: discuss",
				"Speak in the conversation only through an available messaging capability.",
			},
		},
		{
			sessionType: sessionmode.Schedule,
			want: []string{
				"You are an AI agent running inside a private Memoh workspace.",
				"## Session mode: schedule",
				"Your normal text output is logged only.",
			},
		},
		{
			sessionType: sessionmode.Subagent,
			want: []string{
				"You are an AI agent running inside a private Memoh workspace.",
				"## Session mode: subagent",
				"You are a task-focused worker spawned by a parent agent.",
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.sessionType, func(t *testing.T) {
			t.Parallel()
			prompt := GenerateSystemPrompt(SystemPromptParams{
				SessionType: tc.sessionType,
				Timezone:    "UTC",
			})
			for _, want := range tc.want {
				if !strings.Contains(prompt, want) {
					t.Fatalf("expected prompt for %s to contain %q", tc.sessionType, want)
				}
			}
		})
	}
}

func TestGenerateSystemPromptIncludesServiceOwnedBotInfo(t *testing.T) {
	t.Parallel()

	prompt := GenerateSystemPrompt(SystemPromptParams{
		SessionType: sessionmode.Chat,
		Bot: BotInfo{
			ID:          "bot-1",
			Name:        "research-bot",
			DisplayName: "Research Bot",
			Timezone:    "Asia/Shanghai",
		},
		Timezone: "UTC",
	})

	for _, want := range []string{
		"## Bot",
		"Service-provided bot identity.",
		"Use `display_name` as your user-facing name when it is present; otherwise use `name`.",
		"Do not invent another name.",
		`"id": "bot-1"`,
		`"name": "research-bot"`,
		`"display_name": "Research Bot"`,
		`"timezone": "Asia/Shanghai"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q", want)
		}
	}
}

func TestBuildFileSectionsRespectsMaxBytes(t *testing.T) {
	t.Parallel()

	sections := buildFileSections([]SystemFile{
		{
			Filename: "AGENTS.md",
			Content:  "HEAD\n" + strings.Repeat("0123456789", 200) + "\nTAIL",
		},
		{
			Filename: "MEMORY.md",
			Content:  "SECOND_FILE_SHOULD_NOT_FIT",
		},
	}, 512)

	if len(sections) > 512 {
		t.Fatalf("file sections bytes = %d, want <= 512", len(sections))
	}
	if !strings.Contains(sections, "## AGENTS.md") {
		t.Fatalf("file sections missing first file heading:\n%s", sections)
	}
	if !strings.Contains(sections, "[memoh pruned]") {
		t.Fatalf("file sections should include prune marker:\n%s", sections)
	}
}

func TestBuildFileSectionsRespectsLineLimit(t *testing.T) {
	t.Parallel()

	sections := buildFileSections([]SystemFile{
		{
			Filename: "AGENTS.md",
			Content:  strings.Repeat("x\n", 2100),
		},
	}, 32*1024)

	if !strings.Contains(sections, "[memoh pruned]") {
		t.Fatalf("file sections should include prune marker for line-only overflow:\n%s", sections)
	}
}

func TestBuildFileSectionsRespectsCumulativeLineLimit(t *testing.T) {
	t.Parallel()

	sections := buildFileSections([]SystemFile{
		{Filename: "AGENTS.md", Content: strings.Repeat("a\n", 1200)},
		{Filename: "MEMORY.md", Content: strings.Repeat("m\n", 1200)},
	}, 32*1024)

	if got := strings.Count(sections, "\n") + 1; got > 2000 {
		t.Fatalf("file sections lines = %d, want <= 2000", got)
	}
	if !strings.Contains(sections, "[memoh pruned]") {
		t.Fatalf("file sections should include prune marker for cumulative line overflow:\n%s", sections)
	}
}

func TestGenerateSystemPromptOmitsLegacyCoreFiles(t *testing.T) {
	t.Parallel()

	for _, sessionType := range allPromptSessionTypes() {
		sessionType := sessionType
		t.Run(sessionType, func(t *testing.T) {
			t.Parallel()
			prompt := GenerateSystemPrompt(SystemPromptParams{
				SessionType: sessionType,
				Timezone:    "UTC",
			})
			for _, legacy := range []string{"IDENTITY.md", "SOUL.md", "TOOLS.md"} {
				if strings.Contains(prompt, legacy) {
					t.Fatalf("expected prompt for %s to omit legacy file %s", sessionType, legacy)
				}
			}
		})
	}
}

func TestGenerateSystemPromptOmitsToolSpecificMemorySearchGuidance(t *testing.T) {
	t.Parallel()

	for _, sessionType := range allPromptSessionTypes() {
		sessionType := sessionType
		t.Run(sessionType, func(t *testing.T) {
			t.Parallel()
			prompt := GenerateSystemPrompt(SystemPromptParams{
				SessionType: sessionType,
				Timezone:    "UTC",
			})
			if strings.Contains(prompt, "`search_memory`") {
				t.Fatalf("system prompt for %s should not statically mention search_memory, got:\n%s", sessionType, prompt)
			}
			if strings.Contains(prompt, "{{memorySearchSection}}") {
				t.Fatalf("system prompt for %s leaked memorySearchSection placeholder, got:\n%s", sessionType, prompt)
			}
		})
	}
}

// TestGenerateSystemPromptDoesNotReintroduceStaticToolSections enforces the
// unified guidance-source contract: detailed tool availability guidance must
// come from registered tools/ToolUsage, not the old static partial sections.
func TestGenerateSystemPromptDoesNotReintroduceStaticToolSections(t *testing.T) {
	t.Parallel()

	for _, sessionType := range allPromptSessionTypes() {
		sessionType := sessionType
		t.Run(sessionType, func(t *testing.T) {
			t.Parallel()
			prompt := GenerateSystemPrompt(SystemPromptParams{
				SessionType:               sessionType,
				Timezone:                  "UTC",
				PlatformIdentitiesSection: "## Platform Identities\n\n<identity channel=\"telegram\" username=\"@memoh\"/>",
			})
			for _, legacySection := range []string{
				"## Basic Tools",
				"## Contacts & Messaging",
				"## Sessions & History",
				"## Schedule Tasks",
				"## Subagents",
				"## User Input",
			} {
				if strings.Contains(prompt, legacySection) {
					t.Fatalf("system prompt for %s must not include legacy tool section %q", sessionType, legacySection)
				}
			}
		})
	}
}

func TestGenerateSystemPromptDoesNotEnumerateConditionalTools(t *testing.T) {
	t.Parallel()

	conditionalTools := make([]string, 0, len(agenttools.BuiltInToolNames()))
	for _, name := range agenttools.BuiltInToolNames() {
		conditionalTools = append(conditionalTools, name.String())
	}
	sort.Strings(conditionalTools)

	for _, sessionType := range allPromptSessionTypes() {
		sessionType := sessionType
		t.Run(sessionType, func(t *testing.T) {
			t.Parallel()
			prompt := GenerateSystemPrompt(SystemPromptParams{
				SessionType:               sessionType,
				Timezone:                  "UTC",
				PlatformIdentitiesSection: "## Platform Identities\n\n<identity channel=\"telegram\" username=\"@memoh\"/>",
			})
			for _, name := range conditionalTools {
				if promptEnumeratesTool(prompt, name) {
					t.Fatalf("system prompt for %s must not statically enumerate conditional tool %q; put its usage in ToolUsage gated by registration", sessionType, name)
				}
			}
		})
	}
}

func promptEnumeratesTool(prompt, name string) bool {
	token := regexp.QuoteMeta(name)
	if strings.Contains(prompt, "`"+name+"`") {
		return true
	}
	if regexp.MustCompile(`(^|[^A-Za-z0-9_])` + token + `\s*\(`).MatchString(prompt) {
		return true
	}
	if strings.Contains(name, "_") {
		return regexp.MustCompile(`(^|[^A-Za-z0-9_])` + token + `([^A-Za-z0-9_]|$)`).MatchString(prompt)
	}
	switch name {
	case "read":
		if regexp.MustCompile(`(?i)\b(read\s+(and|or)\s+write\s+files?|read\s+files?|reading\s*/\s*writing\s+files?|file\s+reading\s+and\s+writing)\b`).MatchString(prompt) {
			return true
		}
	case "write":
		if regexp.MustCompile(`(?i)\b(read\s+(and|or)\s+write\s+files?|write\s+files?|reading\s*/\s*writing\s+files?|file\s+reading\s+and\s+writing)\b`).MatchString(prompt) {
			return true
		}
	}
	return regexp.MustCompile(`(?i)\b(call|tool|tools|use|using|invoke|invoking|available)\s+` + token + `\b|\b` + token + `\s+(tool|tools|capability|capabilities)\b`).MatchString(prompt)
}

func TestPromptEnumeratesToolDetectsCallSyntax(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		prompt string
		name   string
	}{
		{prompt: "spawn({ tasks: [] })", name: "spawn"},
		{prompt: "send({ text: \"hello\" })", name: "send"},
		{prompt: "read(\"/tmp/image.png\")", name: "read"},
		{prompt: "you can read and write files there freely", name: "read"},
		{prompt: "you can read and write files there freely", name: "write"},
		{prompt: "you can read or write files there freely", name: "read"},
		{prompt: "you can read or write files there freely", name: "write"},
		{prompt: "you can use it for reading/writing files", name: "read"},
		{prompt: "you can use it for reading/writing files", name: "write"},
		{prompt: "you can use it for file reading and writing", name: "read"},
		{prompt: "you can use it for file reading and writing", name: "write"},
	} {
		if !promptEnumeratesTool(tc.prompt, tc.name) {
			t.Fatalf("expected %q to enumerate %q", tc.prompt, tc.name)
		}
	}
}

func TestGenerateSystemPromptIncludesPlatformIdentitiesInDiscuss(t *testing.T) {
	t.Parallel()

	prompt := GenerateSystemPrompt(SystemPromptParams{
		SessionType:               sessionmode.Discuss,
		Timezone:                  "UTC",
		PlatformIdentitiesSection: "## Platform Identities\n\n<identity channel=\"discord\" username=\"@memoh\"/>",
	})

	if !strings.Contains(prompt, "## Platform Identities") {
		t.Fatalf("expected platform identities heading in discuss prompt")
	}
	if !strings.Contains(prompt, `<identity channel="discord" username="@memoh"/>`) {
		t.Fatalf("expected platform identity XML in discuss prompt")
	}
}

func allPromptSessionTypes() []string {
	return []string{
		sessionmode.Chat,
		sessionmode.Discuss,
		sessionmode.Schedule,
		sessionmode.Subagent,
	}
}
