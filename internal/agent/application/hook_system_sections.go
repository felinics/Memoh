package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/hooks"
)

const (
	hookSystemSectionPriority  = 80
	maxHookSystemSectionChars  = 8 * 1024
	maxHookSystemSectionIDPart = 64
	hookSystemSectionSource    = "hook_system_section"
	hookSystemSectionCollector = "hook_system_sections"
)

type promptHookOutput struct {
	Event  string
	Result hooks.Result
}

type hookSystemSectionBuild struct {
	Frags    []contextfrag.ContextFrag
	Warnings []contextfrag.ValidationWarning
}

type hookSystemSectionWarning struct {
	code      string
	message   string
	fragIndex int
}

type hookSystemSectionWarningKey struct {
	hookName string
	id       string
	code     string
}

func buildHookSystemSections(outputs []promptHookOutput, scope contextfrag.Scope) hookSystemSectionBuild {
	usedIDs := make(map[string]struct{})
	var frags []contextfrag.ContextFrag
	var attachedWarnings []hookSystemSectionWarning
	var outputWarnings []contextfrag.ValidationWarning
	for _, output := range outputs {
		attachedWarningCounts := make(map[hookSystemSectionWarningKey]int)
		for _, section := range output.Result.AppendSystemSections {
			id := uniqueHookSystemSectionID(section, usedIDs)
			frag := contextfrag.TextFrag(contextfrag.TextFragInput{
				ID:            id,
				Kind:          contextfrag.KindHookContext,
				Role:          sdk.MessageRoleSystem,
				Slot:          contextfrag.SlotSystem,
				Text:          section.Text,
				Priority:      hookSystemSectionPriority,
				RetentionTier: hookSystemSectionRetention(section.Retention),
				CacheClass:    hookSystemSectionCache(section.Cache),
				Trust:         contextfrag.TrustWorkspace,
				Scope:         scope,
				Source:        hookSystemSectionSource,
				SourceID:      hookSystemSectionSourceID(output.Event, id),
				Collector:     hookSystemSectionCollector,
				Index:         len(frags),
				Render:        contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
				Budget: contextfrag.BudgetPolicy{
					MaxChars: maxHookSystemSectionChars,
					Overflow: contextfrag.OverflowTrim,
				},
			})
			frag.Ref.ID = id
			fragIndex := len(frags)
			frags = append(frags, frag)
			for _, code := range section.WarningCodes {
				attachedWarnings = append(attachedWarnings, hookSystemSectionWarning{
					code:      code,
					message:   hookSystemSectionWarningMessage(code),
					fragIndex: fragIndex,
				})
				attachedWarningCounts[hookSystemSectionWarningKey{
					hookName: section.HookName,
					id:       section.ID,
					code:     code,
				}]++
			}
		}
		for _, warning := range output.Result.Warnings {
			key := hookSystemSectionWarningKey{
				hookName: warning.HookName,
				id:       warning.SectionID,
				code:     warning.Code,
			}
			if attachedWarningCounts[key] > 0 {
				attachedWarningCounts[key]--
				continue
			}
			outputWarnings = append(outputWarnings, contextfrag.ValidationWarning{
				Code:    warning.Code,
				Message: warning.Message,
			})
		}
	}

	frags = contextfrag.NormalizeContextRefs(frags)
	warnings := make([]contextfrag.ValidationWarning, 0, len(attachedWarnings)+len(outputWarnings))
	for _, warning := range attachedWarnings {
		warnings = append(warnings, contextfrag.ValidationWarning{
			Code:    warning.code,
			Message: warning.message,
			Ref:     frags[warning.fragIndex].Ref,
		})
	}
	warnings = append(warnings, outputWarnings...)
	return hookSystemSectionBuild{Frags: frags, Warnings: warnings}
}

func uniqueHookSystemSectionID(section hooks.SystemSectionOutput, used map[string]struct{}) string {
	hookName := boundedHookSystemSectionIDPart(section.HookName, "unnamed")
	base := "system.hook." + hookName
	if declaredID := strings.TrimSpace(section.ID); declaredID != "" {
		base += "." + boundedHookSystemSectionIDPart(declaredID, "section")
	}
	id := base
	for suffix := 2; ; suffix++ {
		if _, exists := used[id]; !exists {
			used[id] = struct{}{}
			return id
		}
		id = fmt.Sprintf("%s.%d", base, suffix)
	}
}

func boundedHookSystemSectionIDPart(raw, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	var token strings.Builder
	token.Grow(min(len(raw), maxHookSystemSectionIDPart))
	changed := false
	for i := 0; i < len(raw); i++ {
		value := raw[i]
		if !hookSystemSectionIDByte(value) {
			value = '-'
			changed = true
		}
		if token.Len() < maxHookSystemSectionIDPart {
			token.WriteByte(value)
		} else {
			changed = true
		}
	}
	value := token.String()
	if value == "" {
		value = fallback
		changed = true
	}
	if !changed && len(value) <= maxHookSystemSectionIDPart {
		return value
	}
	sum := sha256.Sum256([]byte(raw))
	suffix := hex.EncodeToString(sum[:4])
	prefixLimit := maxHookSystemSectionIDPart - len(suffix) - 1
	if len(value) > prefixLimit {
		value = value[:prefixLimit]
	}
	value = strings.TrimRight(value, "-._:")
	if value == "" {
		value = fallback
	}
	return value + "-" + suffix
}

func hookSystemSectionIDByte(value byte) bool {
	return (value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9') ||
		value == '-' || value == '_' || value == '.' || value == ':'
}

func hookSystemSectionRetention(retention hooks.SystemSectionRetention) contextfrag.RetentionTier {
	if retention == hooks.SystemSectionRetentionPreferred {
		return contextfrag.RetentionPreferred
	}
	return contextfrag.RetentionOptional
}

func hookSystemSectionCache(cache hooks.SystemSectionCache) contextfrag.CacheClass {
	if cache == hooks.SystemSectionCacheStable {
		return contextfrag.CacheStable
	}
	return contextfrag.CacheDynamic
}

func hookSystemSectionSourceID(event, id string) string {
	return strings.TrimSpace(event) + ":" + id
}

func hookSystemSectionWarningMessage(code string) string {
	switch code {
	case hooks.WarningSystemSectionRequiredClamped:
		return "hook system section retention was clamped from required to preferred"
	case hooks.WarningAppendSystemSectionOutputLimited:
		return "append_system_section text was limited by max_output_bytes"
	default:
		return ""
	}
}

func hookSystemSectionTexts(result hooks.Result) []string {
	texts := make([]string, 0, len(result.AppendSystemSections))
	for _, section := range result.AppendSystemSections {
		if text := strings.TrimSpace(section.Text); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}
