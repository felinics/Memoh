package native

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/felinics/memoh/internal/agent/sessionmode"
	textprune "github.com/felinics/memoh/internal/prune"
	skillset "github.com/felinics/memoh/internal/skills"
)

//go:embed prompts/*.md
var promptsFS embed.FS

var (
	systemCommonTmpl string
	modeChatTmpl     string
	modeDiscussTmpl  string
	modeScheduleTmpl string
	modeSubagentTmpl string
	scheduleTmpl     string

	includes map[string]string
)

var includeRe = regexp.MustCompile(`\{\{include:(\w+)\}\}`)

func init() {
	systemCommonTmpl = mustReadPrompt("prompts/system_common.md")
	modeChatTmpl = mustReadPrompt("prompts/mode_chat.md")
	modeDiscussTmpl = mustReadPrompt("prompts/mode_discuss.md")
	modeScheduleTmpl = mustReadPrompt("prompts/mode_schedule.md")
	modeSubagentTmpl = mustReadPrompt("prompts/mode_subagent.md")
	scheduleTmpl = mustReadPrompt("prompts/schedule.md")

	includes = map[string]string{
		"_memory":     mustReadPrompt("prompts/_memory.md"),
		"_identities": mustReadPrompt("prompts/_identities.md"),
	}

	systemCommonTmpl = resolveIncludes(systemCommonTmpl)
	modeChatTmpl = resolveIncludes(modeChatTmpl)
	modeDiscussTmpl = resolveIncludes(modeDiscussTmpl)
	modeScheduleTmpl = resolveIncludes(modeScheduleTmpl)
	modeSubagentTmpl = resolveIncludes(modeSubagentTmpl)
}

func mustReadPrompt(name string) string {
	data, err := promptsFS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("failed to read embedded prompt %s: %v", name, err))
	}
	return string(data)
}

// resolveIncludes replaces {{include:_name}} placeholders with the content of the named fragment.
func resolveIncludes(tmpl string) string {
	return includeRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		sub := includeRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		content, ok := includes[sub[1]]
		if !ok {
			return match
		}
		return strings.TrimSpace(content)
	})
}

// render replaces all {{key}} placeholders in tmpl with values from vars.
func render(tmpl string, vars map[string]string) string {
	result := tmpl
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return strings.TrimSpace(result)
}

func selectModeTemplate(sessionType string) string {
	switch sessionType {
	case sessionmode.Discuss:
		return modeDiscussTmpl
	case sessionmode.Schedule:
		return modeScheduleTmpl
	case sessionmode.Subagent:
		return modeSubagentTmpl
	default:
		return modeChatTmpl
	}
}

// GenerateSystemPrompt builds the complete system prompt from files, skills, and context.
func GenerateSystemPrompt(params SystemPromptParams) string {
	return renderSystemSections(GenerateSystemSections(params))
}

// SystemPromptParams holds all inputs for system prompt generation.
type SystemPromptParams struct {
	SessionType               string
	Bot                       BotInfo
	Skills                    []SkillEntry
	Files                     []SystemFile
	MaxFilesBytes             int
	Timezone                  string
	PlatformIdentitiesSection string
	PlatformIdentities        []SystemPromptItem
}

type SystemPromptItem struct {
	ID   string
	Text string
}

func buildBotInfoSection(bot BotInfo) string {
	bot.ID = strings.TrimSpace(bot.ID)
	bot.Name = strings.TrimSpace(bot.Name)
	bot.DisplayName = strings.TrimSpace(bot.DisplayName)
	bot.Timezone = strings.TrimSpace(bot.Timezone)
	if bot.ID == "" && bot.Name == "" && bot.DisplayName == "" && bot.Timezone == "" {
		return ""
	}
	raw, err := json.MarshalIndent(bot, "", "  ")
	if err != nil {
		return ""
	}
	return "## Bot\n\nService-provided bot identity. Use `display_name` as your user-facing name when it is present; otherwise use `name`. `name` is the stable slug. Do not invent another name.\n\n```json\n" + string(raw) + "\n```"
}

// GenerateSchedulePrompt builds the user message for a scheduled task trigger.
func GenerateSchedulePrompt(s Schedule) string {
	maxCallsStr := "Unlimited"
	if s.MaxCalls != nil {
		maxCallsStr = strconv.Itoa(*s.MaxCalls)
	}
	return render(scheduleTmpl, map[string]string{
		"name":        s.Name,
		"description": s.Description,
		"maxCalls":    maxCallsStr,
		"pattern":     s.Pattern,
		"command":     s.Command,
	})
}

func buildSkillsSection(skills []SkillEntry) string {
	items := buildSkillPromptItems(skills)
	if len(items) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(buildSkillsHeader(len(items)))
	for _, item := range items {
		sb.WriteByte('\n')
		sb.WriteString(item.Text)
	}
	return sb.String()
}

func buildSkillPromptItems(skills []SkillEntry) []SystemPromptItem {
	sorted := make([]SkillEntry, len(skills))
	copy(sorted, skills)
	slices.SortFunc(sorted, func(a, b SkillEntry) int {
		return strings.Compare(a.Name, b.Name)
	})
	items := make([]SystemPromptItem, 0, len(sorted))
	for _, skill := range sorted {
		items = append(items, SystemPromptItem{
			ID:   skill.Name,
			Text: "- **" + skill.Name + "**: " + skill.Description,
		})
	}
	return items
}

func buildSkillsHeader(count int) string {
	var sb strings.Builder
	sb.WriteString("## Skills\n\n")
	sb.WriteString("Memoh-managed skills are stored in `" + skillset.ManagedDir() + "/`. ")
	sb.WriteString("Compatible external skill directories inside the bot workspace may also be discovered automatically. ")
	sb.WriteString("Each skill is represented by a `SKILL.md` file in one of the discovered source directories. ")
	sb.WriteString("Only activate a skill when it is relevant to the current task and a skill-loading capability is available.\n\n")
	sb.WriteString(strconv.Itoa(count))
	sb.WriteString(" skill(s) available:")
	return sb.String()
}

func buildFileSections(files []SystemFile, maxBytes int) string {
	items := buildFilePromptItems(files, maxBytes)
	texts := make([]string, 0, len(items))
	for _, item := range items {
		texts = append(texts, item.Text)
	}
	return strings.Join(texts, "\n\n")
}

func buildFilePromptItems(files []SystemFile, maxBytes int) []SystemPromptItem {
	maxBytes = normalizeSystemFilesMaxBytes(maxBytes)
	var items []SystemPromptItem
	totalBytes := 0
	lineCount := 0
	for _, f := range files {
		if f.Content == "" {
			continue
		}
		separator := ""
		separatorLines := 0
		if len(items) > 0 {
			separator = "\n\n"
			separatorLines = 2
		}
		remaining := maxBytes - totalBytes - len(separator)
		remainingLines := textprune.DefaultMaxLines - lineCount - separatorLines
		if remaining <= 0 || remainingLines <= 0 {
			break
		}
		section := formatSystemFile(f)
		if textprune.Exceeds(section, remaining, remainingLines) {
			truncated, ok := truncateSystemFileSection(f, remaining, remainingLines)
			if !ok {
				break
			}
			section = truncated
		}
		items = append(items, SystemPromptItem{ID: f.Filename, Text: section})
		totalBytes += len(separator) + len(section)
		lineCount += separatorLines + textprune.CountLines(section)
		if len(section) == remaining {
			break
		}
	}
	return items
}

func normalizeSystemFilesMaxBytes(maxBytes int) int {
	if maxBytes <= 0 {
		return DefaultSystemFilesMaxBytes
	}
	return maxBytes
}

func truncateSystemFileSection(file SystemFile, maxBytes, maxLines int) (string, bool) {
	heading := fmt.Sprintf("## %s\n\n", file.Filename)
	headingLines := textprune.CountLines(heading)
	if maxBytes <= len(heading) || maxLines <= headingLines {
		return "", false
	}
	contentBudget := maxBytes - len(heading)
	content := textprune.PruneWithEdges(file.Content, systemFilePruneLabel(file), systemFilePruneConfig(contentBudget, maxLines-headingLines))
	return heading + content, true
}

func systemFilePruneLabel(file SystemFile) string {
	filename := strings.TrimSpace(file.Filename)
	if filename == "" {
		return "workspace file"
	}
	return "workspace file " + filename
}

func systemFilePruneConfig(maxBytes, maxLines int) textprune.Config {
	headBytes, tailBytes := splitHeadTail(maxBytes)
	headLines, tailLines := splitHeadTail(maxLines)
	return textprune.Config{
		MaxBytes:  maxBytes,
		MaxLines:  maxLines,
		HeadBytes: headBytes,
		TailBytes: tailBytes,
		HeadLines: headLines,
		TailLines: tailLines,
		Marker:    textprune.DefaultMarker,
	}
}

func splitHeadTail(maxBytes int) (int, int) {
	headBytes := maxBytes * 3 / 4
	tailBytes := maxBytes - headBytes
	if headBytes <= 0 {
		return maxBytes, 0
	}
	return headBytes, tailBytes
}

func formatSystemFile(file SystemFile) string {
	return fmt.Sprintf("## %s\n\n%s", file.Filename, file.Content)
}
