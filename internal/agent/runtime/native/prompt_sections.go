package native

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/agent/sessionmode"
)

// SystemSection is one typed, priority-ordered piece of the system prompt.
type SystemSection struct {
	ID       string
	Kind     contextfrag.Kind
	Priority int
	Text     string
}

const (
	sectionIDIntro                 = "system.prompt.intro"
	sectionIDBotIdentity           = "system.bot_identity"
	sectionIDBody                  = "system.prompt.body"
	sectionIDTail                  = "system.prompt.tail"
	sectionIDPlatformIdentity      = "system.platform_identity"
	sectionIDSkills                = "system.skills"
	sectionIDWorkspaceInstructions = "system.workspace_instructions"
	sectionIDFallback              = "system.prompt.fallback"
)

const (
	priorityIntro                 = 10
	priorityBotIdentity           = 20
	priorityBody                  = 30
	priorityTail                  = 50
	priorityPlatformIdentity      = 60
	prioritySkills                = 65
	priorityWorkspaceInstructions = 70
)

const (
	botInfoPlaceholder = "{{botInfoSection}}"
	workspaceHeading   = "## Workspace instruction files"
)

// GenerateSystemSections builds the typed system prompt sections that
// renderSystemSections joins into the same string GenerateSystemPrompt used
// to build directly. Two sections (intro, bot identity) sit inside a single
// template's own placeholder gaps: they are always returned, even with empty
// Text, so the uniform join below reproduces that template's literal
// spacing. Sections folding in dynamic, possibly-absent content (platform
// identity, skills, workspace files) are omitted entirely when empty.
func GenerateSystemSections(params SystemPromptParams) []SystemSection {
	home := "/data"
	timezone := strings.TrimSpace(params.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}

	intro, body, tailBoilerplate, err := splitSystemCommonTmpl(systemCommonTmpl)
	if err != nil {
		return degradedSystemSections(params, home, timezone, err)
	}
	isSubagent := params.SessionType == sessionmode.Subagent

	tail, err := buildSystemPromptTail(tailBoilerplate, params.SessionType, isSubagent)
	if err != nil {
		return degradedSystemSections(params, home, timezone, err)
	}

	sections := []SystemSection{
		{
			ID: sectionIDIntro, Kind: contextfrag.KindSystemPrompt, Priority: priorityIntro,
			Text: render(intro, map[string]string{"home": home, "timezone": timezone}),
		},
		{
			ID: sectionIDBotIdentity, Kind: contextfrag.KindBotIdentity, Priority: priorityBotIdentity,
			Text: buildBotInfoSection(params.Bot),
		},
		{ID: sectionIDBody, Kind: contextfrag.KindSystemPrompt, Priority: priorityBody, Text: body},
		{ID: sectionIDTail, Kind: contextfrag.KindSystemPrompt, Priority: priorityTail, Text: tail},
	}

	if text := strings.TrimSpace(params.PlatformIdentitiesSection); text != "" {
		sections = append(sections, SystemSection{
			ID: sectionIDPlatformIdentity, Kind: contextfrag.KindPlatformIdentity, Priority: priorityPlatformIdentity, Text: text,
		})
	}

	if isSubagent {
		return sections
	}

	if text := strings.TrimSpace(buildSkillsSection(params.Skills)); text != "" {
		sections = append(sections, SystemSection{
			ID: sectionIDSkills, Kind: contextfrag.KindSkillsCatalog, Priority: prioritySkills, Text: text,
		})
	}
	if text := strings.TrimSpace(buildFileSections(params.Files, params.MaxFilesBytes)); text != "" {
		sections = append(sections, SystemSection{
			ID: sectionIDWorkspaceInstructions, Kind: contextfrag.KindWorkspaceInstruction, Priority: priorityWorkspaceInstructions, Text: text,
		})
	}

	return sections
}

// degradedSystemSections is the panic-free fallback for when the embedded
// prompt templates are missing an anchor GenerateSystemSections needs to
// split them into typed sections. It logs the failure and returns the
// complete, unsplit template text as a single KindSystemPrompt section
// instead of crashing Agent.Stream's un-recovered goroutine.
//
// It still runs the whole system_common+mode template through the same
// render() substitution the healthy path applies (per section), so a
// missing anchor degrades to an unsplit prompt instead of one that leaks a
// literal {{placeholder}} or drops the bot identity.
func degradedSystemSections(params SystemPromptParams, home, timezone string, cause error) []SystemSection {
	slog.Default().Error("agent: system prompt template missing expected anchor; falling back to an unsplit system prompt", slog.Any("error", cause))
	tmpl := strings.TrimSpace(systemCommonTmpl + "\n\n" + selectModeTemplate(params.SessionType))
	text := render(tmpl, map[string]string{
		"home":              home,
		"timezone":          timezone,
		"botInfoSection":    buildBotInfoSection(params.Bot),
		"mainAgentSections": "",
		"subagentSections":  "",
	})
	if params.SessionType != sessionmode.Subagent {
		text += "\n\n" + strings.TrimSpace(includes["_memory"])
	}
	return []SystemSection{{
		ID:       sectionIDFallback,
		Kind:     contextfrag.KindSystemPrompt,
		Priority: priorityBody,
		Text:     text,
	}}
}

// SystemSectionFrags converts typed system prompt sections into context
// fragments, mirroring contextview.ToolUsageFrag's construction so a caller
// building ContextSourceFrags directly from GenerateSystemSections produces
// fragments indistinguishable in shape from the rest of the pipeline.
func SystemSectionFrags(sections []SystemSection, scope contextfrag.Scope) []contextfrag.ContextFrag {
	frags := make([]contextfrag.ContextFrag, 0, len(sections))
	for _, section := range sections {
		frags = append(frags, contextfrag.TextFrag(contextfrag.TextFragInput{
			ID:         section.ID,
			Kind:       section.Kind,
			Role:       sdk.MessageRoleSystem,
			Slot:       contextfrag.SlotSystem,
			Text:       section.Text,
			Priority:   section.Priority,
			CacheClass: contextfrag.CacheStable,
			Trust:      contextfrag.TrustSystem,
			Scope:      scope,
			Source:     contextfrag.SourceRunConfig,
			Collector:  "system_sections",
			Render:     contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
		}))
	}
	return frags
}

// renderSystemSections sorts sections by Priority ascending and joins them
// with a blank line between every consecutive pair. Emptiness is already
// resolved at construction time (which section a piece is, and whether it
// was appended at all), so the join never special-cases an empty Text.
func renderSystemSections(sections []SystemSection) string {
	sorted := slices.Clone(sections)
	slices.SortFunc(sorted, func(a, b SystemSection) int {
		return a.Priority - b.Priority
	})
	var sb strings.Builder
	for i, s := range sorted {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(s.Text)
	}
	return sb.String()
}

// splitSystemCommonTmpl cuts the raw system_common.md template at the two
// anchors that also matter downstream (the bot-identity placeholder and the
// workspace-instructions heading), so the pieces can become independent
// sections instead of being reverse-parsed from the rendered output later.
// It returns an error instead of panicking when either anchor is missing.
func splitSystemCommonTmpl(tmpl string) (intro, body, tailBoilerplate string, err error) {
	idxBotInfo := strings.Index(tmpl, botInfoPlaceholder)
	idxWorkspace := strings.Index(tmpl, workspaceHeading)
	if idxBotInfo < 0 || idxWorkspace < 0 {
		return "", "", "", errors.New("agent: system_common.md is missing an expected section anchor")
	}
	intro = tmpl[:idxBotInfo]
	body = strings.TrimSpace(tmpl[idxBotInfo+len(botInfoPlaceholder) : idxWorkspace])
	tailBoilerplate = tmpl[idxWorkspace:]
	return intro, body, tailBoilerplate, nil
}

// buildSystemPromptTail fuses the static workspace-instructions/attachments
// boilerplate with the mode contract and, for main-agent modes, the memory
// section — mirroring the "\n\n" joins GenerateSystemPrompt used to produce
// via raw template concatenation and joinPromptSections.
func buildSystemPromptTail(tailBoilerplate, sessionType string, isSubagent bool) (string, error) {
	contract, err := modeContractTmpl(sessionType)
	if err != nil {
		return "", err
	}
	tail := tailBoilerplate + "\n\n" + contract
	if isSubagent {
		return tail, nil
	}
	return tail + "\n\n" + strings.TrimSpace(includes["_memory"]), nil
}

// modeContractTmpl returns the mode-specific contract text with its trailing
// mainAgentSections/subagentSections placeholder cut off and trimmed.
func modeContractTmpl(sessionType string) (string, error) {
	tmpl := selectModeTemplate(sessionType)
	placeholder := "{{mainAgentSections}}"
	if sessionType == sessionmode.Subagent {
		placeholder = "{{subagentSections}}"
	}
	return cutModeContractTmpl(tmpl, placeholder)
}

// cutModeContractTmpl cuts tmpl at placeholder, returning the trimmed prefix.
// It returns an error instead of panicking when placeholder is not present.
func cutModeContractTmpl(tmpl, placeholder string) (string, error) {
	idx := strings.Index(tmpl, placeholder)
	if idx < 0 {
		return "", fmt.Errorf("agent: mode template is missing expected placeholder %s", placeholder)
	}
	return strings.TrimSpace(tmpl[:idx]), nil
}
