package botbackup

import (
	"context"

	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db"
)

// summaryLightSections is the section set collected in full for the export
// dialog. History and assets are intentionally excluded: their counts/samples
// are fetched cheaply via dedicated COUNT queries instead of loading every
// message and asset row just to display a number.
var summaryLightSections = []Section{
	SectionSettings, SectionModels, SectionACL, SectionChannels,
	SectionMCP, SectionSchedules, SectionEmail,
}

// Summary reports what a live bot would export: per-section item counts and a
// sample of item labels. It powers the export dialog so users see counts and
// details (and skip empty sections) before exporting. Unlike Export it does not
// pause the bot or stream the workspace, and it counts history/assets/workspace
// without loading them in full.
func (s *Service) Summary(ctx context.Context, botID string) (SummaryResult, error) {
	data, _, err := s.collect(ctx, botID, ExportOptions{Sections: summaryLightSections})
	if err != nil {
		return SummaryResult{}, err
	}
	res := SummaryResult{Sections: []SectionSummary{}}
	if prof, err := roundTripJSON[bots.Bot](data.Profile); err == nil {
		res.Profile = &ProfilePreview{
			DisplayName: prof.DisplayName,
			AvatarURL:   prof.AvatarURL,
			Timezone:    prof.Timezone,
			IsActive:    prof.IsActive,
		}
	}
	add := func(key Section, value any, labelKeys ...string) {
		raw, _ := marshalJSON(value)
		res.Sections = append(res.Sections, SectionSummary{
			Key:       key,
			Count:     jsonArrayLen(raw),
			Items:     jsonArrayLabels(raw, sectionItemLimit, labelKeys...),
			Sensitive: isSensitiveSection(key),
		})
	}
	// settings.json backs two cards: behavior settings + model config.
	settingsRaw, _ := marshalJSON(data.Settings)
	res.Sections = append(res.Sections, SectionSummary{
		Key:   SectionSettings,
		Count: 1,
		Items: settingsLabels(settingsRaw),
	})
	modelsRaw, _ := marshalJSON(data.Dependencies.Models)
	res.Sections = append(res.Sections, SectionSummary{
		Key:       SectionModels,
		Count:     jsonArrayLen(modelsRaw),
		Sensitive: true,
		Items:     jsonArrayLabels(modelsRaw, sectionItemLimit, "name", "model_id"),
	})
	add(SectionACL, data.ACLRules, "description", "subject_channel_type")
	add(SectionChannels, data.Channels, "channel_type")
	add(SectionMCP, data.MCP, "name")
	add(SectionSchedules, data.Schedules, "name")
	add(SectionEmail, data.EmailBindings, "email_address")
	res.Sections = append(res.Sections, s.summarizeHistory(ctx, botID))
	res.Sections = append(res.Sections, s.summarizeAssets(ctx, botID))
	res.Sections = append(res.Sections, s.summarizeWorkspace(ctx, botID))
	return res, nil
}

// summarizeHistory counts messages with a COUNT query and lists session titles
// (sessions are bounded) as the sample, avoiding a full message load.
func (s *Service) summarizeHistory(ctx context.Context, botID string) SectionSummary {
	out := SectionSummary{Key: SectionHistory}
	if s.queries == nil {
		return out
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return out
	}
	if n, err := s.queries.CountMessagesByBot(ctx, pgBotID); err == nil {
		out.Count = int(n)
	}
	if sessions, err := s.queries.ListSessionsByBot(ctx, pgBotID); err == nil {
		raw, _ := marshalJSON(sessions)
		out.Items = jsonArrayLabels(raw, sectionItemLimit, "title", "type")
	}
	return out
}

// summarizeAssets counts message assets with a COUNT query.
func (s *Service) summarizeAssets(ctx context.Context, botID string) SectionSummary {
	out := SectionSummary{Key: SectionAssets}
	if s.queries == nil {
		return out
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return out
	}
	if n, err := s.queries.CountMessageAssetsByBot(ctx, pgBotID); err == nil {
		out.Count = int(n)
	}
	return out
}

// summarizeWorkspace reports the live workspace file count. When the container
// is stopped or unreachable the count is unknown (-1) so the dialog still
// offers the section without a misleading zero.
func (s *Service) summarizeWorkspace(ctx context.Context, botID string) SectionSummary {
	count := -1
	if s.workspace != nil {
		if n, err := s.workspace.CountData(ctx, botID); err == nil {
			count = n
		}
	}
	return SectionSummary{Key: SectionWorkspace, Count: count}
}
