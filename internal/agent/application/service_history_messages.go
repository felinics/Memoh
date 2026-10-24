package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// buildMessagesFromPipeline assembles chat context from the DCP pipeline's
// RenderedContext (RC) merged with assistant/tool turns (TR) from
// bot_history_messages. This gives chat mode the same event-driven context
// that discuss mode uses, replacing the legacy loadMessages path. Both
// streams start at the session's history origin, under the notice of what it
// left out. The second return value is the token pressure of raw history,
// active summaries and the notice. A history or frontier load failure fails
// the request (CM-ADM-003).
func (s *Service) buildMessagesFromPipeline(ctx context.Context, req ChatRequest, contextTokenBudget int) ([]ModelMessage, int, error) {
	sessionID := strings.TrimSpace(req.ThreadID)
	if s.pipeline == nil || sessionID == "" {
		return nil, 0, nil
	}
	rc := s.pipeline.GetRC(sessionID)
	if len(rc) == 0 {
		return nil, 0, nil
	}

	trs, err := s.loadTurnResponses(ctx, sessionID, contextTokenBudget)
	if err != nil {
		return nil, 0, fmt.Errorf("load turn history: %w", err)
	}
	artifacts, err := s.loadTimelineArtifacts(ctx, req.BotID, sessionID)
	if err != nil {
		return nil, 0, fmt.Errorf("load compaction frontier: %w", err)
	}
	origin, err := s.historyOrigin(ctx, sessionID)
	if err != nil {
		return nil, 0, err
	}

	composed := timeline.ComposeContextWithArtifacts(rc, trs, artifacts, origin)
	if composed == nil {
		return nil, 0, nil
	}

	messages := make([]ModelMessage, 0, len(composed.Messages))
	pressure := 0
	for _, m := range composed.Messages {
		if m.Source != nil && req.ExternalMessageID != "" {
			m.Source.Current = m.Source.Kind == "external" && m.Source.ID == req.ExternalMessageID
		}
		if m.CompactionArtifactID != "" {
			m.Source = &turn.ContextMessageSource{Kind: "summary", ID: m.CompactionArtifactID}
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
		pressure += estimateMessageTokens(messages[len(messages)-1])
	}

	return messages, pressure, nil
}

// loadTimelineArtifacts projects the session's active compaction frontier for
// timeline composition.
func (s *Service) loadTimelineArtifacts(ctx context.Context, botID, sessionID string) ([]timeline.CompactionArtifact, error) {
	if s.queries == nil {
		return nil, nil
	}
	return compaction.NewTimelineArtifactSource(s.queries).ActiveCompactionArtifacts(ctx, botID, sessionID)
}

// loadTurnResponses loads assistant/tool messages of the thread's history,
// from its compaction anchor on, for use as the TR stream in pipeline-based context assembly; the frontier's
// artifacts stand for the older ones. The load is byte-budgeted on the
// database side (CM-ADM-001): rows are admitted newest-first within the
// context budget and projected to content, so a long history, or legacy
// lifecycle metadata on its rows, can never materialize more than the
// budget's worth of payload.
func (s *Service) loadTurnResponses(ctx context.Context, sessionID string, contextTokenBudget int) ([]timeline.TurnResponseEntry, error) {
	if s.messageService == nil {
		return nil, nil
	}
	anchor, err := s.messageService.HistoryAnchorBySession(ctx, sessionID, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("load history anchor: %w", err)
	}
	msgs, err := s.messageService.ListTurnResponseSourcesSinceBySessionWithinBytes(ctx, sessionID, anchor, s.historyLoadMaxBytes(contextTokenBudget))
	if err != nil {
		return nil, err
	}
	entries := timeline.DecodeTurnResponseEntries(msgs)
	stored := make(map[string]messagepkg.Message, len(msgs))
	for _, msg := range msgs {
		stored[msg.ID] = msg
	}
	for i := range entries {
		if msg, ok := stored[entries[i].SourceMessageID]; ok {
			entries[i].ContentHash = historyfrag.MessageContentHash(historyfrag.DecodeStoredModelMessage(nil, msg.ID, msg.Role, msg.Content))
		}
	}
	return entries, nil
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
