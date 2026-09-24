package native

import (
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/sessionmode"
)

const (
	staticPromptTokenHeadroom           = 4
	staticPromptChatBaselineTokens      = 1212
	staticPromptDiscussBaselineTokens   = 1278
	staticPromptScheduleBaselineTokens  = 1220
	staticPromptSubagentBaselineTokens  = 599
	staticPromptChatPreRound6Tokens     = 1224
	staticPromptDiscussPreRound6Tokens  = 1297
	staticPromptSchedulePreRound6Tokens = 1226
	staticPromptSubagentPreRound6Tokens = 617
)

var staticPromptTokenBaselines = map[string]int{
	sessionmode.Chat:     staticPromptChatBaselineTokens,
	sessionmode.Discuss:  staticPromptDiscussBaselineTokens,
	sessionmode.Schedule: staticPromptScheduleBaselineTokens,
	sessionmode.Subagent: staticPromptSubagentBaselineTokens,
}

var staticPromptPreRound6Tokens = map[string]int{
	sessionmode.Chat:     staticPromptChatPreRound6Tokens,
	sessionmode.Discuss:  staticPromptDiscussPreRound6Tokens,
	sessionmode.Schedule: staticPromptSchedulePreRound6Tokens,
	sessionmode.Subagent: staticPromptSubagentPreRound6Tokens,
}

func TestStaticPromptSizeBaselines(t *testing.T) {
	t.Parallel()

	for _, mode := range allPromptSessionTypes() {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			prompt := renderSystemSections(GenerateSystemSections(SystemPromptParams{
				SessionType: mode,
				Timezone:    "UTC",
			}))
			got := contextfrag.TokensFromBytes(len(prompt))
			baseline, ok := staticPromptTokenBaselines[mode]
			if !ok {
				t.Fatalf("missing static prompt baseline for mode %q", mode)
			}
			preRound6, ok := staticPromptPreRound6Tokens[mode]
			if !ok {
				t.Fatalf("missing pre-Round-6 static prompt size for mode %q", mode)
			}
			t.Logf("static prompt tokens = %d, bytes = %d", got, len(prompt))
			if baseline >= preRound6 || baseline+staticPromptTokenHeadroom >= preRound6 {
				t.Fatalf(
					"static prompt baseline/headroom = %d/%d, pre-Round-6 = %d",
					baseline,
					staticPromptTokenHeadroom,
					preRound6,
				)
			}
			if got > baseline+staticPromptTokenHeadroom {
				t.Fatalf(
					"static prompt tokens = %d, baseline = %d, headroom = %d",
					got,
					baseline,
					staticPromptTokenHeadroom,
				)
			}
		})
	}
}

func TestGenerateSystemSectionsPolicyForRemainingNativeModes(t *testing.T) {
	t.Parallel()

	want := []struct {
		id        string
		kind      contextfrag.Kind
		priority  int
		retention contextfrag.RetentionTier
	}{
		{"system.prompt.intro", contextfrag.KindSystemPrompt, 10, contextfrag.RetentionRequired},
		{"system.bot_identity", contextfrag.KindBotIdentity, 20, contextfrag.RetentionPreferred},
		{"system.prompt.body", contextfrag.KindSystemPrompt, 30, contextfrag.RetentionRequired},
		{"system.prompt.tail", contextfrag.KindSystemPrompt, 50, contextfrag.RetentionRequired},
		{"system.platform_identity.header", contextfrag.KindPlatformIdentity, 60, contextfrag.RetentionPreferred},
		{"system.platform_identity.telegram-1", contextfrag.KindPlatformIdentity, 60, contextfrag.RetentionPreferred},
		{"system.skills.header", contextfrag.KindSkillsCatalog, 65, contextfrag.RetentionOptional},
		{"system.skill.bar-skill", contextfrag.KindSkillsCatalog, 65, contextfrag.RetentionOptional},
		{"system.skill.foo-skill", contextfrag.KindSkillsCatalog, 65, contextfrag.RetentionOptional},
		{"system.workspace_file.AGENTS.md", contextfrag.KindWorkspaceInstruction, 70, contextfrag.RetentionPreferred},
		{"system.workspace_file.PROFILES.md", contextfrag.KindWorkspaceInstruction, 70, contextfrag.RetentionPreferred},
	}

	for _, mode := range []string{sessionmode.Discuss, sessionmode.Schedule} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			sections := GenerateSystemSections(SystemPromptParams{
				SessionType:               mode,
				Timezone:                  "UTC",
				Bot:                       goldenFullBot,
				Skills:                    goldenFullSkills,
				Files:                     goldenFullFiles,
				PlatformIdentitiesSection: goldenFullPlatform,
				PlatformIdentities:        goldenFullPlatformItems,
			})
			if len(sections) != len(want) {
				t.Fatalf("sections = %#v, want %d entries", sections, len(want))
			}
			for i := range want {
				if sections[i].ID != want[i].id ||
					sections[i].Kind != want[i].kind ||
					sections[i].Priority != want[i].priority ||
					sections[i].RetentionTier != want[i].retention {
					t.Fatalf("section[%d] = %#v, want %#v", i, sections[i], want[i])
				}
			}
		})
	}
}
