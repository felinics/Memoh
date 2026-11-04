package timeline

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/agent/turn"
)

// TurnResponseEntry represents an assistant or tool message from bot_history_messages,
// used as the "TR" stream in context composition.
type TurnResponseEntry struct {
	RequestedAtMs   int64           `json:"requested_at_ms"`
	Role            string          `json:"role"`
	Content         string          `json:"content"`
	RawContent      json.RawMessage `json:"raw_content,omitempty"`
	SourceMessageID string          `json:"source_message_id,omitempty"`
	// DiscussStep reports a row a Discuss run wrote before its answer:
	// context, not proof the run consumed its input.
	DiscussStep bool `json:"-"`
}

// ContextMessage is a unified message for LLM context, produced by MergeContext.
// Pinned marks what composition keeps whatever the budget: what the
// session's checkpoint holds.
type ContextMessage struct {
	Source               *turn.ContextMessageSource `json:"source,omitempty"`
	Role                 string                     `json:"role"`
	Content              string                     `json:"content"`
	RawContent           json.RawMessage            `json:"raw_content,omitempty"`
	CompactionArtifactID string                     `json:"compaction_artifact_id,omitempty"`
	Pinned               bool                       `json:"pinned,omitempty"`
}

// ComposeContextResult holds the output of ComposeContext.
type ComposeContextResult struct {
	Messages        []ContextMessage
	EstimatedTokens int
}

type mergeEntry struct {
	source *turn.ContextMessageSource
	kind   string // "rc", "summary", or "tr"
	time   int64
	step   int
	// pinned entries open the history in a fixed order: what the checkpoint
	// holds.
	pinned bool
	// For RC entries
	rcContent []RenderedContentPiece
	// For summary entries
	summaryContent string
	summaryID      string
	// For TR entries
	trRole       string
	trContent    string
	trRawContent json.RawMessage
}

func appendRenderedContextEntries(entries []mergeEntry, rc RenderedContext) []mergeEntry {
	for i, seg := range rc {
		entries = append(entries, renderedContextEntry(seg, 2*i))
	}
	return entries
}

func renderedContextEntry(seg RenderedSegment, step int) mergeEntry {
	return mergeEntry{
		kind:      "rc",
		time:      seg.ReceivedAtMs,
		step:      step,
		rcContent: seg.Content,
		source:    renderedMessageSource(seg),
	}
}

func appendTurnResponseEntries(entries []mergeEntry, trs []TurnResponseEntry) []mergeEntry {
	for i, tr := range trs {
		entries = append(entries, turnResponseEntry(tr, 2*i))
	}
	return entries
}

func turnResponseEntry(tr TurnResponseEntry, step int) mergeEntry {
	return mergeEntry{
		kind:         "tr",
		time:         tr.RequestedAtMs,
		step:         step,
		trRole:       tr.Role,
		source:       &turn.ContextMessageSource{Kind: "history", ID: tr.SourceMessageID},
		trContent:    tr.Content,
		trRawContent: tr.RawContent,
	}
}

func mergeEntries(entries []mergeEntry) []ContextMessage {
	markCurrentEntries(entries, nil, nil)
	return materializeMergeEntries(entries)
}

func sortMergeEntries(entries []mergeEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].time != entries[j].time {
			return entries[i].time < entries[j].time
		}
		leftOrder, rightOrder := mergeKindOrder(entries[i].kind), mergeKindOrder(entries[j].kind)
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return entries[i].step < entries[j].step
	})
}

func mergeKindOrder(kind string) int {
	switch kind {
	case "rc":
		return 0
	case "tr":
		return 1
	default:
		return 2
	}
}

func materializeMergeEntries(entries []mergeEntry) []ContextMessage {
	var messages []ContextMessage

	for _, entry := range entries {
		switch entry.kind {
		case "rc":
			var pendingText strings.Builder
			for _, piece := range entry.rcContent {
				if piece.Type == "text" {
					if pendingText.Len() > 0 {
						pendingText.WriteByte('\n')
					}
					pendingText.WriteString(piece.Text)
				}
			}
			if pendingText.Len() > 0 {
				messages = append(messages, ContextMessage{Role: "user", Content: pendingText.String(), Source: entry.source, Pinned: entry.pinned})
			}
		case "summary":
			messages = append(messages, ContextMessage{
				Role:                 "user",
				Content:              entry.summaryContent,
				CompactionArtifactID: entry.summaryID,
				Pinned:               true,
			})
		case "tr":
			messages = append(messages, ContextMessage{
				Role:       entry.trRole,
				Source:     entry.source,
				Content:    entry.trContent,
				RawContent: entry.trRawContent,
				Pinned:     entry.pinned,
			})
		}
	}

	return messages
}

// ComposeContextWithCheckpoint composes a session's history under its
// checkpoint: what the checkpoint holds in its order, and then, by time, the
// segments it leaves raw and trs, the turn responses after its position.
func ComposeContextWithCheckpoint(rc RenderedContext, trs []TurnResponseEntry, checkpoint HistoryCheckpoint) *ComposeContextResult {
	allMessages := mergeEntries(composeMergeEntries(rc, trs, checkpoint))
	if len(allMessages) == 0 {
		return nil
	}

	return &ComposeContextResult{
		Messages:        allMessages,
		EstimatedTokens: estimateMessagesTokens(allMessages),
	}
}

// composeMergeEntries builds the ordered merge-entry list for composition.
// Entries hold references into rc/trs plus small summary strings; nothing
// large is materialized here, so admission can measure before it selects.
// A segment the checkpoint folds without a change since does not compose
// (HistoryCheckpoint.composes); a row the checkpoint keeps renders at its
// place among what it holds, its segment for an inbound message, and nowhere
// else.
func composeMergeEntries(rc RenderedContext, trs []TurnResponseEntry, checkpoint HistoryCheckpoint) []mergeEntry {
	held := make(map[string]int)
	for i, item := range checkpoint.Items {
		if item.MessageID != "" {
			held[item.MessageID] = i
		}
	}
	segments := make([]*RenderedSegment, len(checkpoint.Items))
	rest := make(RenderedContext, 0, len(rc))
	for i := range rc {
		id := strings.TrimSpace(rc[i].MessageID)
		if index, ok := held[id]; ok {
			segments[index] = &rc[i]
			continue
		}
		if !checkpoint.composes(rc[i]) {
			continue
		}
		rest = append(rest, rc[i])
	}
	entries := make([]mergeEntry, 0, len(checkpoint.Items)+len(rest)+len(trs))
	for i, item := range checkpoint.Items {
		var entry mergeEntry
		switch {
		case item.SummaryID != "":
			entry = mergeEntry{
				kind:           "summary",
				summaryContent: turn.SummaryText(item.Summary),
				summaryID:      item.SummaryID,
			}
		case item.Response != nil:
			entry = turnResponseEntry(*item.Response, i)
		case segments[i] != nil:
			entry = renderedContextEntry(*segments[i], i)
		default:
			continue
		}
		entry.pinned = true
		entries = append(entries, entry)
	}
	pinned := len(entries)
	entries = appendRenderedContextEntries(entries, rest)
	entries = appendTurnResponseEntries(entries, trs)
	sortMergeEntries(entries[pinned:])
	return entries
}

// ComposeBudget bounds composition before materialization (CM-ADM-001).
type ComposeBudget struct {
	After *DiscussCursorPosition
	// MaxTokens is the admission budget in shared-estimator tokens. Zero or
	// negative disables budgeting (legacy behavior).
	MaxTokens int
}

// ComposeAdmission reports the admission decision made for one composition.
type ComposeAdmission struct {
	// EstimatedTokens is the pre-selection estimate of the full raw context.
	EstimatedTokens int
	// SelectedTokens is the estimate of what was actually materialized.
	SelectedTokens int
	// CurrentTokens is the cost of the protected current input. OmittedSources
	// lists older unconsumed input the selection left out; recovery must
	// compact it or the run must record its omission.
	CurrentTokens  int
	OmittedSources []turn.ContextMessageSource
	// TotalEntries and DroppedEntries count merge entries, not messages.
	TotalEntries   int
	DroppedEntries int
	// ProtectedOverflow is set when the protected set alone (the pinned
	// entries plus the newest entry) exceeds the budget; the caller must
	// fail closed instead of materializing (CM-ADM-002).
	ProtectedOverflow bool
}

// ComposeContextWithCheckpointBudgeted composes like
// ComposeContextWithCheckpoint but makes the admission decision on entry
// metadata before materializing anything. Over budget it keeps every pinned
// entry plus the newest contiguous window that fits, dropping older raw
// entries deterministically. When even the protected set does not fit it
// returns a nil result with ProtectedOverflow set.
func ComposeContextWithCheckpointBudgeted(
	rc RenderedContext,
	trs []TurnResponseEntry,
	checkpoint HistoryCheckpoint,
	budget ComposeBudget,
) (*ComposeContextResult, ComposeAdmission) {
	entries := composeMergeEntries(rc, trs, checkpoint)
	if len(entries) == 0 {
		return nil, ComposeAdmission{}
	}
	markCurrentEntries(entries, rc, budget.After)

	admitted := make([]turn.AdmissionEntry, len(entries))
	for i := range entries {
		admitted[i] = turn.AdmissionEntry{
			Cost:   mergeEntryTokens(entries[i]),
			Source: entries[i].source,
			Pinned: entries[i].pinned,
			ToolResponse: entries[i].kind == "tr" &&
				strings.EqualFold(strings.TrimSpace(entries[i].trRole), "tool"),
		}
	}
	decision := turn.AdmitContextEntries(admitted, budget.MaxTokens)
	admission := ComposeAdmission{
		EstimatedTokens:   decision.EstimatedTokens,
		SelectedTokens:    decision.SelectedTokens,
		TotalEntries:      len(entries),
		DroppedEntries:    decision.DroppedEntries,
		ProtectedOverflow: decision.ProtectedOverflow,
	}
	for i, protected := range turn.ProtectedAdmissionEntries(admitted, budget.MaxTokens) {
		if protected {
			admission.CurrentTokens += admitted[i].Cost
		}
	}
	if decision.ProtectedOverflow {
		return nil, admission
	}
	for i, current := range turn.CurrentAdmissionEntries(admitted) {
		if current && !decision.Selected[i] {
			admission.OmittedSources = append(admission.OmittedSources, *entries[i].source)
		}
	}
	kept := entries
	if decision.DroppedEntries > 0 {
		kept = make([]mergeEntry, 0, len(entries)-decision.DroppedEntries)
		for i := range entries {
			if decision.Selected[i] {
				kept = append(kept, entries[i])
			}
		}
	}
	messages := materializeMergeEntries(kept)
	if len(messages) == 0 {
		return nil, admission
	}
	return &ComposeContextResult{Messages: messages, EstimatedTokens: decision.SelectedTokens}, admission
}

// mergeEntryTokens estimates one entry's cost from lengths alone, without
// materializing the entry.
func mergeEntryTokens(entry mergeEntry) int {
	switch entry.kind {
	case "rc":
		n := 0
		for _, piece := range entry.rcContent {
			if piece.Type == "text" {
				if n > 0 {
					n++
				}
				n += len(piece.Text)
			}
		}
		return turn.EstimateTokensFromBytes(n)
	case "tr":
		if len(entry.trRawContent) > 0 {
			return turn.EstimateTokensFromBytes(len(entry.trRawContent))
		}
		return turn.EstimateTokensFromBytes(len(entry.trContent))
	default:
		return turn.EstimateTokensFromBytes(len(entry.summaryContent))
	}
}

func estimateMessagesTokens(messages []ContextMessage) int {
	total := 0
	for _, m := range messages {
		total += estimateMessageTokens(m)
	}
	return total
}

func estimateMessageTokens(m ContextMessage) int {
	if len(m.RawContent) > 0 {
		return turn.EstimateTokensFromBytes(len(m.RawContent))
	}
	return turn.EstimateTokensFromBytes(len(m.Content))
}
