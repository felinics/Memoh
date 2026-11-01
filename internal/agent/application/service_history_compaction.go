package application

import (
	"context"
	"fmt"
	"slices"
	"strings"

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
// its source, or else the record it was loaded from, matched in order by
// content (a summary renders its compaction log). A message that renders the
// request's own input is an anchor.
func contextProvenance(messages []ModelMessage, records []historyfrag.HistoryRecord, inputs []turn.ContextMessageSource) []native.MessageProvenance {
	matched := matchHistoryRecords(messages, records)
	var out []native.MessageProvenance
	for i, message := range messages {
		var source turn.ContextMessageSource
		switch index := matched[i]; {
		case message.ContextSource != nil:
			source = *message.ContextSource
		case index >= 0 && records[index].SourceKind == historyfrag.SourceCompactionLog:
			source = turn.ContextMessageSource{Kind: "summary", ID: records[index].Ref.ID}
		case index >= 0:
			source = turn.ContextMessageSource{Kind: "history", ID: strings.TrimSpace(records[index].DBMessageID)}
		default:
			continue
		}
		sent := modelMessagesToSDKMessages([]ModelMessage{message})
		if len(sent) != 1 {
			continue
		}
		anchor := source.Current || slices.ContainsFunc(inputs, func(input turn.ContextMessageSource) bool {
			return input.Kind == source.Kind && input.ID == source.ID
		})
		source.Current = false
		out = append(out, native.MessageProvenance{
			Message: sent[0],
			Sources: []turn.ContextMessageSource{source},
			Anchor:  anchor,
		})
	}
	return out
}

// matchHistoryRecords pairs each message, in order, with the index of the
// stored record it was loaded from (a row, or the compaction log a summary
// renders), or -1.
func matchHistoryRecords(messages []ModelMessage, records []historyfrag.HistoryRecord) []int {
	matched := make([]int, len(messages))
	for i := range matched {
		matched[i] = -1
	}
	positions := make(map[string][]int, len(records))
	for i, record := range records {
		if strings.TrimSpace(record.DBMessageID) == "" && record.SourceKind != historyfrag.SourceCompactionLog {
			continue
		}
		key := modelMessageSourceKey(record.ModelMessage)
		positions[key] = append(positions[key], i)
	}
	cursors := make(map[string]int, len(positions))
	lastMatched := -1
	for i, message := range messages {
		key := modelMessageSourceKey(message)
		candidates := positions[key]
		cursor := cursors[key]
		for cursor < len(candidates) && candidates[cursor] <= lastMatched {
			cursor++
		}
		cursors[key] = cursor
		if cursor >= len(candidates) {
			continue
		}
		matched[i] = candidates[cursor]
		cursors[key] = cursor + 1
		lastMatched = matched[i]
	}
	return matched
}

func modelMessageSourceKey(message ModelMessage) string {
	if hash := historyfrag.MessageContentHash(message); hash != "" {
		return hash
	}
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
