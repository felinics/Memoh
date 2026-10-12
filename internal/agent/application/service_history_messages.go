package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// buildMessagesFromPipeline assembles chat context from the DCP pipeline's
// RenderedContext (RC) merged with assistant/tool turns (TR) from
// bot_history_messages. This gives chat mode the same event-driven context
// that discuss mode uses, replacing the legacy loadMessages path. The second
// return value is the raw (compactable) token pressure measured before
// trimming; the third also includes active summaries.
func (s *Service) buildMessagesFromPipeline(ctx context.Context, req ChatRequest, contextTokenBudget int) ([]ModelMessage, int, int) {
	sessionID := strings.TrimSpace(req.ThreadID)
	if s.pipeline == nil || sessionID == "" {
		return nil, 0, 0
	}
	rc := s.pipeline.GetRC(sessionID)
	if len(rc) == 0 {
		return nil, 0, 0
	}

	trs := s.loadTurnResponses(ctx, sessionID, contextTokenBudget)
	artifacts := s.loadTimelineArtifacts(ctx, req.BotID, sessionID)

	composed := timeline.ComposeContextWithArtifacts(rc, trs, artifacts)
	if composed == nil {
		return nil, 0, 0
	}

	messages := make([]ModelMessage, 0, len(composed.Messages))
	pinned := make([]bool, 0, len(composed.Messages))
	compactable, pressure := 0, 0
	for _, m := range composed.Messages {
		if m.Source != nil && req.ExternalMessageID != "" {
			m.Source.Current = m.Source.Kind == "external" && m.Source.ID == req.ExternalMessageID
		}
		contentJSON := m.RawContent
		if len(contentJSON) == 0 {
			var err error
			contentJSON, err = json.Marshal(m.Content)
			if err != nil {
				continue
			}
		}
		messages = append(messages, ModelMessage{
			Role:          m.Role,
			ContextSource: m.Source,
			Content:       contentJSON,
		})
		isPinned := m.CompactionArtifactID != ""
		pinned = append(pinned, isPinned)
		cost := estimateMessageTokens(messages[len(messages)-1])
		pressure += cost
		if !isPinned {
			compactable += cost
		}
	}

	// Apply context token budget trimming to pipeline path as well.
	if contextTokenBudget > 0 && len(messages) > 0 {
		messages = trimPipelineMessagesByTokens(s.logger, messages, pinned, contextTokenBudget)
	}

	return messages, compactable, pressure
}

// loadTimelineArtifacts projects the session's active compaction frontier for
// timeline composition. A load failure degrades into the bounded admission
// window — the pipeline trim below still caps the recomposed raw history —
// and is surfaced with a stable key instead of passing silently (CM-ADM-003).
func (s *Service) loadTimelineArtifacts(ctx context.Context, botID, sessionID string) []timeline.CompactionArtifact {
	if s.queries == nil {
		return nil
	}
	artifacts, err := compaction.NewTimelineArtifactSource(s.queries).ActiveCompactionArtifacts(ctx, botID, sessionID)
	if err != nil {
		s.logger.WarnContext(ctx, "context_admission_degraded",
			slog.String("path", "pipeline_chat"),
			slog.String("reason", "artifact_load_failed"),
			slog.String("session_id", sessionID),
			slog.Any("error", err))
		return nil
	}
	return artifacts
}

// trimPipelineMessagesByTokens trims pipeline-assembled messages to fit within
// the context token budget using character-based estimation. Pinned messages
// (compaction summaries) survive the dropped prefix.
func trimPipelineMessagesByTokens(log *slog.Logger, messages []ModelMessage, pinned []bool, maxTokens int) []ModelMessage {
	entries := make([]turn.AdmissionEntry, len(messages))
	for i, message := range messages {
		entries[i] = turn.AdmissionEntry{Source: message.ContextSource, Cost: estimateMessageTokens(message), Pinned: i < len(pinned) && pinned[i], ToolResponse: strings.EqualFold(strings.TrimSpace(message.Role), "tool")}
	}
	decision := turn.AdmitContextEntries(entries, maxTokens)
	if decision.ProtectedOverflow || decision.DroppedEntries == 0 {
		// Keep irreducible input intact for the final provider admission.
		return messages
	}
	kept := make([]ModelMessage, 0, len(messages)-decision.DroppedEntries)
	for i, message := range messages {
		if decision.Selected[i] {
			kept = append(kept, message)
		}
	}

	if log != nil {
		log.Info("trimPipelineMessagesByTokens: context trimmed",
			slog.Int("total_messages", len(messages)),
			slog.Int("estimated_tokens", decision.EstimatedTokens),
			slog.Int("max_tokens", maxTokens),
			slog.Int("kept_messages", len(kept)),
		)
	}

	return kept
}

// loadTurnResponses loads recent assistant/tool messages from bot_history_messages
// for use as the TR stream in pipeline-based context assembly. The load is
// byte-budgeted on the database side (CM-ADM-001): rows are admitted
// newest-first within the context budget and projected to content, so a
// dense 24-hour window, or legacy lifecycle metadata on its rows, can never
// materialize more than the budget's worth of payload.
func (s *Service) loadTurnResponses(ctx context.Context, sessionID string, contextTokenBudget int) []timeline.TurnResponseEntry {
	if s.messageService == nil {
		return nil
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	msgs, err := s.messageService.ListTurnResponseSourcesSinceBySessionWithinBytes(ctx, sessionID, since, s.historyLoadMaxBytes(contextTokenBudget))
	if err != nil {
		s.logger.WarnContext(ctx, "load TRs failed", slog.String("session_id", sessionID), slog.Any("error", err))
		return nil
	}
	return timeline.DecodeTurnResponseEntries(msgs)
}

// historyLoadMaxBytes converts a token budget into the byte budget used for
// database-side history admission; a missing budget falls back to the
// absolute cap so no load path is ever unbounded.
func (s *Service) historyLoadMaxBytes(contextTokenBudget int) int64 {
	if contextTokenBudget <= 0 {
		contextTokenBudget = s.contextAbsoluteMaxTokens()
	}
	return int64(contextTokenBudget) * contextfrag.EstimateBytesPerToken
}
