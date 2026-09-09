package compaction

import (
	"context"
	"strings"

	"github.com/felinics/memoh/internal/chat/timeline"
)

// TimelineArtifactSource loads the active artifact frontier of a session as
// timeline projections for context composition.
type TimelineArtifactSource struct {
	projection ArtifactProjection
}

func NewTimelineArtifactSource(queries artifactProjectionQueries) TimelineArtifactSource {
	return TimelineArtifactSource{projection: NewArtifactProjection(queries)}
}

func (s TimelineArtifactSource) ActiveCompactionArtifacts(ctx context.Context, botID, sessionID string) ([]timeline.CompactionArtifact, error) {
	frontier, err := s.projection.LoadActiveSession(ctx, ArtifactOwner{
		BotID:          botID,
		SessionID:      sessionID,
		SessionIDKnown: true,
	})
	if err != nil {
		return nil, err
	}
	return TimelineArtifacts(frontier.Artifacts), nil
}

// TimelineArtifacts projects active artifacts onto the timeline contract.
// Artifacts without a trustworthy summary or coverage must not swallow their
// original sources, and legacy artifacts without any coverage cannot replace
// anything — injecting them would duplicate the history they summarize — so
// both are omitted entirely.
func TimelineArtifacts(artifacts []Artifact) []timeline.CompactionArtifact {
	projected := make([]timeline.CompactionArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.CoverageMalformed || len(artifact.Coverage) == 0 || strings.TrimSpace(artifact.Summary) == "" {
			continue
		}
		sources := make([]timeline.CompactionSource, 0, len(artifact.Coverage))
		for _, source := range artifact.Coverage {
			sources = append(sources, timeline.CompactionSource{
				HistoryMessageID:  source.Ref.ID,
				ExternalMessageID: source.ExternalMessageID,
				CreatedAtMs:       source.CreatedAtMs,
			})
		}
		coverageAsOfMs := int64(0)
		if !artifact.StartedAt.IsZero() {
			coverageAsOfMs = artifact.StartedAt.UnixMilli()
		}
		projected = append(projected, timeline.CompactionArtifact{
			ID:             artifact.ID,
			Summary:        artifact.Summary,
			AnchorStartMs:  artifact.AnchorStartMs,
			CoverageAsOfMs: coverageAsOfMs,
			Sources:        sources,
		})
	}
	return projected
}
