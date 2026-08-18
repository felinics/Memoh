package native

import (
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/agent/sessionmode"
)

func TestGenerateSystemPromptMatchesLegacyAssemblyBytes(t *testing.T) {
	t.Parallel()
	full := SystemPromptParams{
		Timezone: "UTC",
		Bot:      BotInfo{ID: "bot-1", Name: "research-bot", DisplayName: "Research Bot", Timezone: "Asia/Shanghai"},
		Skills: []SkillEntry{
			{Name: "foo-skill", Description: "does foo things"},
			{Name: "bar-skill", Description: "does bar things"},
		},
		Files: []SystemFile{
			{Filename: "AGENTS.md", Content: "# Agent notes\n\nBe nice."},
			{Filename: "PROFILES.md", Content: "# People\n\n- Alice"},
		},
		PlatformIdentitiesSection: "## Platform Identities\n\n<identity channel=\"telegram\" username=\"@memoh\"/>",
	}
	for _, mode := range []string{sessionmode.Chat, sessionmode.Discuss, sessionmode.Schedule, sessionmode.Subagent} {
		mode := mode
		t.Run(mode+"_full", func(t *testing.T) {
			t.Parallel()
			params := full
			params.SessionType = mode
			assertSystemPromptLegacyEquivalent(t, params)
		})
		t.Run(mode+"_empty", func(t *testing.T) {
			t.Parallel()
			assertSystemPromptLegacyEquivalent(t, SystemPromptParams{SessionType: mode, Timezone: "UTC"})
		})
	}
	mixed := []SystemPromptParams{
		{SessionType: sessionmode.Chat, Timezone: "UTC", Bot: full.Bot, Files: full.Files},
		{SessionType: sessionmode.Chat, Timezone: "UTC", Skills: full.Skills, PlatformIdentitiesSection: full.PlatformIdentitiesSection},
	}
	for _, params := range mixed {
		assertSystemPromptLegacyEquivalent(t, params)
	}
}

func assertSystemPromptLegacyEquivalent(t *testing.T, params SystemPromptParams) {
	t.Helper()
	want := legacyGenerateSystemPrompt(params)
	if got := GenerateSystemPrompt(params); got != want {
		t.Fatalf("GenerateSystemPrompt mismatch\ngot:  %q\nwant: %q", got, want)
	}
	if got := renderSystemSections(GenerateSystemSections(params)); got != want {
		t.Fatalf("section render mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

func legacyGenerateSystemPrompt(params SystemPromptParams) string {
	home := "/data"
	timezone := strings.TrimSpace(params.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	skills := buildSkillsSection(params.Skills)
	files := buildFileSections(params.Files, params.MaxFilesBytes)
	platform := strings.TrimSpace(params.PlatformIdentitiesSection)
	tmpl := strings.TrimSpace(systemCommonTmpl + "\n\n" + selectModeTemplate(params.SessionType))
	return render(tmpl, map[string]string{
		"home":                      home,
		"timezone":                  timezone,
		"botInfoSection":            buildBotInfoSection(params.Bot),
		"skillsSection":             skills,
		"platformIdentitiesSection": platform,
		"mainAgentSections":         legacyMainAgentSections(platform, skills, files),
		"subagentSections":          legacySubagentSections(platform),
		"fileSections":              files,
	})
}

func legacyMainAgentSections(platform, skills, files string) string {
	identities := render(includes["_identities"], map[string]string{"platformIdentitiesSection": platform})
	return legacyJoinPromptSections(includes["_memory"], identities, skills, files)
}

func legacySubagentSections(platform string) string {
	return strings.TrimSpace(render(includes["_identities"], map[string]string{"platformIdentitiesSection": platform}))
}

func legacyJoinPromptSections(sections ...string) string {
	var kept []string
	for _, section := range sections {
		if section = strings.TrimSpace(section); section != "" {
			kept = append(kept, section)
		}
	}
	return strings.Join(kept, "\n\n")
}

func TestGenerateSystemSectionsShape(t *testing.T) {
	t.Parallel()
	sections := GenerateSystemSections(SystemPromptParams{
		SessionType:               sessionmode.Chat,
		Timezone:                  "UTC",
		Bot:                       BotInfo{ID: "bot-1", Name: "research-bot"},
		Skills:                    []SkillEntry{{Name: "foo", Description: "does foo"}},
		Files:                     []SystemFile{{Filename: "AGENTS.md", Content: "Be nice."}},
		PlatformIdentitiesSection: "platform header\nplatform identity",
		PlatformIdentities:        []SystemPromptItem{{ID: "telegram-1", Text: "platform identity"}},
	})
	want := []struct {
		id                 string
		kind               contextfrag.Kind
		priority           int
		retention          contextfrag.RetentionTier
		requiredCapability string
	}{
		{sectionIDIntro, contextfrag.KindSystemPrompt, priorityIntro, contextfrag.RetentionRequired, ""},
		{sectionIDBotIdentity, contextfrag.KindBotIdentity, priorityBotIdentity, contextfrag.RetentionPreferred, ""},
		{sectionIDBody, contextfrag.KindSystemPrompt, priorityBody, contextfrag.RetentionRequired, ""},
		{sectionIDTail, contextfrag.KindSystemPrompt, priorityTail, contextfrag.RetentionRequired, ""},
		{sectionIDPlatformIdentity + ".header", contextfrag.KindPlatformIdentity, priorityPlatformIdentity, contextfrag.RetentionPreferred, ""},
		{sectionIDPlatformIdentity + ".telegram-1", contextfrag.KindPlatformIdentity, priorityPlatformIdentity, contextfrag.RetentionPreferred, ""},
		{sectionIDSkills + ".header", contextfrag.KindSkillsCatalog, prioritySkills, contextfrag.RetentionOptional, "use_skill"},
		{sectionIDSkill + ".foo", contextfrag.KindSkillsCatalog, prioritySkills, contextfrag.RetentionOptional, "use_skill"},
		{sectionIDWorkspaceFile + ".AGENTS.md", contextfrag.KindWorkspaceInstruction, priorityWorkspaceInstructions, contextfrag.RetentionPreferred, ""},
	}
	if len(sections) != len(want) {
		t.Fatalf("sections = %#v", sections)
	}
	for i := range want {
		if sections[i].ID != want[i].id || sections[i].Kind != want[i].kind || sections[i].Priority != want[i].priority ||
			sections[i].RetentionTier != want[i].retention || sections[i].RequiredCapability != want[i].requiredCapability {
			t.Fatalf("section[%d] = %#v, want %#v", i, sections[i], want[i])
		}
	}
}

func TestGenerateSystemSectionsGranularDynamicItemsRemainByteEquivalent(t *testing.T) {
	t.Parallel()

	platformItems := []SystemPromptItem{
		{ID: "telegram-1", Text: `<identity channel="telegram" username="@memoh"/>`},
		{ID: "微信-2", Text: `<identity channel="weixin" username="小明"/>`},
	}
	platformSection := "## Platform Identities\n\nKnown identities.\n\n" +
		platformItems[0].Text + "\n" + platformItems[1].Text
	skills := []SkillEntry{
		{Name: "技能", Description: "第二"},
		{Name: "alpha", Description: "first"},
	}
	files := []SystemFile{
		{Filename: "ZETA.md", Content: "zeta"},
		{Filename: "AGENTS.md", Content: "agents"},
		{Filename: "MEMORY.md", Content: "still included on the accepted PR1 path"},
	}
	params := SystemPromptParams{
		SessionType:               sessionmode.Chat,
		Timezone:                  "UTC",
		Skills:                    skills,
		Files:                     files,
		PlatformIdentitiesSection: platformSection,
		PlatformIdentities:        platformItems,
	}

	sections := GenerateSystemSections(params)
	wantIDs := []string{
		"system.prompt.intro",
		"system.bot_identity",
		"system.prompt.body",
		"system.prompt.tail",
		"system.platform_identity.header",
		"system.platform_identity.telegram-1",
		"system.platform_identity.微信-2",
		"system.skills.header",
		"system.skill.alpha",
		"system.skill.技能",
		"system.workspace_file.ZETA.md",
		"system.workspace_file.AGENTS.md",
		"system.workspace_file.MEMORY.md",
	}
	gotIDs := make([]string, 0, len(sections))
	for _, section := range sections {
		gotIDs = append(gotIDs, section.ID)
	}
	if strings.Join(gotIDs, "\n") != strings.Join(wantIDs, "\n") {
		t.Fatalf("section IDs = %v, want %v", gotIDs, wantIDs)
	}

	wantSuffix := platformSection + "\n\n" +
		buildSkillsSection(skills) + "\n\n" +
		buildFileSections(files, DefaultSystemFilesMaxBytes)
	if got := GenerateSystemPrompt(params); !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("granular prompt suffix mismatch\ngot:  %q\nwant suffix: %q", got, wantSuffix)
	}
}

func TestGenerateSystemSectionsSkillsRequireUseSkillCapability(t *testing.T) {
	t.Parallel()

	sections := GenerateSystemSections(SystemPromptParams{
		SessionType: sessionmode.Chat,
		Skills:      []SkillEntry{{Name: "alpha", Description: "first"}},
	})
	found := 0
	for _, section := range sections {
		if section.Kind != contextfrag.KindSkillsCatalog {
			continue
		}
		found++
		if section.RequiredCapability != skillRequiredCapability {
			t.Fatalf("%s required capability = %q, want %q", section.ID, section.RequiredCapability, skillRequiredCapability)
		}
	}
	if found != 2 {
		t.Fatalf("skill sections = %d, want header plus item", found)
	}
}

func TestGenerateSystemSectionsKeepsOnlyStructuralEmptySection(t *testing.T) {
	t.Parallel()
	sections := GenerateSystemSections(SystemPromptParams{SessionType: sessionmode.Chat, Timezone: "UTC"})
	foundBot := false
	for _, section := range sections {
		switch section.Kind {
		case contextfrag.KindBotIdentity:
			foundBot = true
			if section.Text != "" {
				t.Fatalf("bot identity text = %q", section.Text)
			}
		case contextfrag.KindPlatformIdentity, contextfrag.KindSkillsCatalog, contextfrag.KindWorkspaceInstruction:
			t.Fatalf("empty optional section survived: %#v", section)
		}
	}
	if !foundBot {
		t.Fatal("missing structural bot identity section")
	}
}

func TestSystemSectionFragsPreserveTypedShape(t *testing.T) {
	t.Parallel()
	sections := []SystemSection{
		{
			ID: "a", Kind: contextfrag.KindSystemPrompt, Priority: 10, Text: " first ",
			RetentionTier: contextfrag.RetentionPreferred, DropPriority: 40, RequiredCapability: "read",
			Render: contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown, GroupID: "group", GroupJoiner: "\n"},
		},
		{ID: "b", Kind: contextfrag.KindBotIdentity, Priority: 20},
	}
	frags := SystemSectionFrags(sections, contextfrag.Scope{BotID: "bot-1"})
	if len(frags) != 2 {
		t.Fatalf("frags = %#v", frags)
	}
	for i, frag := range frags {
		if frag.ID != sections[i].ID || frag.Kind != sections[i].Kind || frag.Priority != sections[i].Priority ||
			frag.Role != sdk.MessageRoleSystem || frag.Slot != contextfrag.SlotSystem || frag.Scope.BotID != "bot-1" ||
			frag.Parts[0].Text != contextfrag.RenderText(sections[i].Text, sections[i].Render) ||
			frag.RetentionTier != sections[i].RetentionTier || frag.DropPriority != sections[i].DropPriority ||
			frag.RequiredCapability != sections[i].RequiredCapability {
			t.Fatalf("frag[%d] = %#v", i, frag)
		}
		wantRender := sections[i].Render
		if wantRender.Format == "" {
			wantRender.Format = contextfrag.RenderMarkdown
		}
		if frag.Render != wantRender {
			t.Fatalf("frag[%d] render policy = %#v, want %#v", i, frag.Render, wantRender)
		}
	}
}

func TestGenerateSystemSectionsDegradesWhenAnchorsAreMissing(t *testing.T) {
	t.Run("system template", func(t *testing.T) {
		original := systemCommonTmpl
		systemCommonTmpl = strings.Replace(original, workspaceHeading, "## renamed", 1)
		t.Cleanup(func() { systemCommonTmpl = original })
		sections := GenerateSystemSections(SystemPromptParams{SessionType: sessionmode.Chat, Bot: BotInfo{Name: "research-bot"}})
		assertDegradedSection(t, sections)
	})
	t.Run("mode template", func(t *testing.T) {
		original := modeChatTmpl
		modeChatTmpl = strings.Replace(original, "{{mainAgentSections}}", "", 1)
		t.Cleanup(func() { modeChatTmpl = original })
		sections := GenerateSystemSections(SystemPromptParams{SessionType: sessionmode.Chat, Bot: BotInfo{Name: "research-bot"}})
		assertDegradedSection(t, sections)
	})
}

func assertDegradedSection(t *testing.T, sections []SystemSection) {
	t.Helper()
	if len(sections) != 1 || sections[0].Kind != contextfrag.KindSystemPrompt ||
		sections[0].RetentionTier != contextfrag.RetentionRequired ||
		strings.Contains(sections[0].Text, "{{") || !strings.Contains(sections[0].Text, "research-bot") {
		t.Fatalf("sections = %#v", sections)
	}
}

func TestSectionSplitHelpersRejectMissingAnchors(t *testing.T) {
	t.Parallel()
	if _, _, _, err := splitSystemCommonTmpl("no anchors"); err == nil {
		t.Fatal("expected splitSystemCommonTmpl error")
	}
	if _, err := cutModeContractTmpl("no placeholder", "{{missing}}"); err == nil {
		t.Fatal("expected cutModeContractTmpl error")
	}
}
