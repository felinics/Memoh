package native

import (
	"fmt"
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

// Service-authored notices for auxiliary context a turn could not load. They
// tell the model what is unknown so it neither invents the missing context
// nor claims checks or actions that did not run.
const (
	WorkspaceUnavailableNotice = "The workspace is unavailable for this turn. Its hooks did not run, and its skills and instruction files (AGENTS.md, MEMORY.md, PROFILES.md) could not be read, so their contents are unknown. Continue as a text-only conversation: tools, file access and commands are disabled, and the selected workspace has not changed. Do not claim any workspace action ran."
	HooksUnavailableNotice     = "The hooks configuration could not be loaded for this turn, so no hooks ran and tools are disabled. Continue as a text-only conversation; do not claim hook checks or actions ran."
	SkillsUnavailableNotice    = "Skills discovery could not be completed for this turn. The installed skills are unknown or incomplete; do not infer that none are installed."
)

// SystemFileUnavailable marks a workspace instruction file that exists or may
// exist but could not be read. A missing file is simply empty.
const SystemFileUnavailable = "unavailable"

const sectionIDLoadStatus = "system.auxiliary_load_status"

func loadStatusSections(params SystemPromptParams) []SystemSection {
	text := buildLoadStatusText(params)
	if text == "" {
		return nil
	}
	return []SystemSection{{
		ID:            sectionIDLoadStatus,
		Kind:          contextfrag.KindSystemPrompt,
		Priority:      priorityPlatformIdentity,
		RetentionTier: contextfrag.RetentionRequired,
		Text:          text,
	}}
}

func buildLoadStatusText(params SystemPromptParams) string {
	items := append([]string(nil), params.LoadNotices...)
	for _, file := range params.Files {
		if file.LoadStatus == SystemFileUnavailable {
			items = append(items, fmt.Sprintf("%s could not be read for this turn. Its contents are unknown; do not infer that it is empty or that no instructions or memory exist.", file.Filename))
		}
	}
	seen := make(map[string]bool, len(items))
	lines := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		lines = append(lines, "- "+item)
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Context loading status\n\nService-provided status for this turn.\n\n" + strings.Join(lines, "\n")
}
