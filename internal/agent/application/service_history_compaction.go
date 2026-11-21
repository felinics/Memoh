package application

import (
	"context"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// historyContextFragsForMessages is the fragment of each summary the
// messages render, at the message that renders it.
func historyContextFragsForMessages(messages []ModelMessage, records []historyfrag.HistoryRecord) []contextfrag.ContextFrag {
	var frags []contextfrag.ContextFrag
	for i, index := range matchHistoryRecords(messages, records) {
		if index < 0 || records[index].Kind != contextfrag.KindConversationSummary {
			continue
		}
		frag := historyfrag.ToFrag(records[index])
		frag.ID = fmt.Sprintf("message.%03d", i)
		frag.Provenance.Index = i
		frags = append(frags, frag)
	}
	return frags
}

func historySourceMessageIDsForMessages(messages []ModelMessage, records []historyfrag.HistoryRecord) []string {
	sourceIDs := make([]string, len(messages))
	for i, index := range matchHistoryRecords(messages, records) {
		if index >= 0 {
			sourceIDs[i] = strings.TrimSpace(records[index].DBMessageID)
		}
	}
	return sourceIDs
}

// contextProvenance names what each message of a request renders, for a run
// that compacts in place: the inbound message or row a message carries as
// its source, or else the compaction log a summary renders. sent is messages
// in provider form. A message that renders the request's own input is an
// anchor.
func contextProvenance(messages []ModelMessage, sent []sdk.Message, records []historyfrag.HistoryRecord, inputs []turn.ContextMessageSource) []native.MessageProvenance {
	matched := matchHistoryRecords(messages, records)
	var out []native.MessageProvenance
	for i, message := range messages {
		var source turn.ContextMessageSource
		switch index := matched[i]; {
		case message.ContextSource != nil:
			source = *message.ContextSource
		case index >= 0:
			source = turn.ContextMessageSource{Kind: "summary", ID: records[index].Ref.ID}
		default:
			continue
		}
		anchor := source.Current || slices.ContainsFunc(inputs, func(input turn.ContextMessageSource) bool {
			return input.Kind == source.Kind && input.ID == source.ID
		})
		source.Current = false
		out = append(out, native.MessageProvenance{
			Message: sent[i],
			Sources: []turn.ContextMessageSource{source},
			Anchor:  anchor,
		})
	}
	return out
}

// matchHistoryRecords pairs each message with the index of the stored record
// it was loaded from, or -1. A message loaded from a row names that row as
// its source, however a replay projection rewrote it; a summary names none
// and is matched, in order, to the compaction log whose rendering it carries.
func matchHistoryRecords(messages []ModelMessage, records []historyfrag.HistoryRecord) []int {
	rows := make(map[string]int, len(records))
	summaries := make(map[string][]int)
	for i, record := range records {
		if record.SourceKind == historyfrag.SourceCompactionLog {
			key := summaryMatchKey(record.ModelMessage)
			summaries[key] = append(summaries[key], i)
			continue
		}
		if id := strings.TrimSpace(record.DBMessageID); id != "" {
			if _, seen := rows[id]; !seen {
				rows[id] = i
			}
		}
	}
	matched := make([]int, len(messages))
	for i, message := range messages {
		matched[i] = -1
		if source := message.ContextSource; source != nil && source.Kind == "history" {
			if index, ok := rows[strings.TrimSpace(source.ID)]; ok {
				matched[i] = index
			}
			continue
		}
		if len(summaries) == 0 {
			continue
		}
		key := summaryMatchKey(message)
		if candidates := summaries[key]; len(candidates) > 0 {
			matched[i] = candidates[0]
			summaries[key] = candidates[1:]
		}
	}
	return matched
}

func summaryMatchKey(message ModelMessage) string {
	return strings.ToLower(strings.TrimSpace(message.Role)) + "\x00" + string(message.Content)
}

// historyCut is where an edit or retry replays history: the checkpoint its
// replacement keeps, and the history before its cutoff message. The zero
// historyCut is the session's history under its active checkpoint.
type historyCut struct {
	replacedTurnID string
	before         *messagepkg.HistoryPosition
}

// loadHistoryCut resolves the cutoff of an edit or retry; no cutoff message
// means no cut. A cutoff that does not resolve fails the request rather than
// replaying the wrong history (CM-ADM-003).
func (s *Service) loadHistoryCut(ctx context.Context, req ChatRequest) (historyCut, error) {
	messageID := strings.TrimSpace(req.HistoryCutoffBeforeMessageID)
	if messageID == "" || s == nil || s.messageService == nil || strings.TrimSpace(req.ThreadID) == "" {
		return historyCut{}, nil
	}
	position, err := s.messageService.HistoryPositionByID(ctx, req.ThreadID, messageID)
	if err != nil {
		return historyCut{}, fmt.Errorf("load history cutoff: %w", err)
	}
	cut := historyCut{before: &position}
	if req.TurnReplacement != nil {
		cut.replacedTurnID = strings.TrimSpace(req.TurnReplacement.OldTurnID)
	}
	return cut, nil
}
