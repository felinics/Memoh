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
// that discuss mode uses, replacing the legacy loadMessages path. The second
// return value is the token pressure of raw history and active summaries;
// a history or frontier load failure fails the request (CM-ADM-003).
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

	composed := timeline.ComposeContextWithArtifacts(rc, trs, artifacts)
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
	if s.queries == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	return compaction.NewTimelineArtifactSource(s.queries).ActiveCompactionArtifacts(ctx, botID, sessionID)
}

// loadTurnResponses loads assistant/tool history regardless of age. The
// database walks the existing session index newest-first until its byte
// budget is crossed, without measuring the entire old backlog. The TR
// projection excludes legacy lifecycle audits; final admission still applies.
func (s *Service) loadTurnResponses(ctx context.Context, sessionID string, contextTokenBudget int) ([]timeline.TurnResponseEntry, error) {
	if s.messageService == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	since := time.Unix(0, 0)
	msgs, err := s.messageService.ListTurnResponseSourcesSinceBySessionWithinBytes(ctx, sessionID, since, s.historyLoadMaxBytes(contextTokenBudget))
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
