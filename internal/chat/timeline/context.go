package timeline

import (
	"encoding/json"
	"slices"
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
	// ContentHash names the stored version the entry renders. The Agent
	// loader sets it; a compaction refuses a source whose version changed.
	ContentHash string `json:"-"`
	// DiscussStep reports a row a Discuss run wrote before its answer:
	// context, not proof the run consumed its input.
	DiscussStep bool `json:"-"`
}

// ContextMessage is a unified message for LLM context, produced by MergeContext.
type ContextMessage struct {
	Source               *turn.ContextMessageSource `json:"source,omitempty"`
	Role                 string                     `json:"role"`
	Content              string                     `json:"content"`
	RawContent           json.RawMessage            `json:"raw_content,omitempty"`
	CompactionArtifactID string                     `json:"compaction_artifact_id,omitempty"`
}

// ComposeContextResult holds the output of ComposeContext.
type ComposeContextResult struct {
	Messages        []ContextMessage
	EstimatedTokens int
}

// CompactionSource identifies one durable history source covered by an active
// compaction artifact. ExternalMessageID projects that source onto the rendered
// stream; HistoryMessageID projects it onto persisted turn responses.
type CompactionSource struct {
	HistoryMessageID  string `json:"history_message_id,omitempty"`
	ExternalMessageID string `json:"external_message_id,omitempty"`
	CreatedAtMs       int64  `json:"created_at_ms,omitempty"`
	CoverageAsOfMs    *int64 `json:"coverage_as_of_ms,omitempty"`
	EventCursor       int64  `json:"event_cursor,omitempty"`
}

// CompactionArtifact is the timeline-facing projection of one active compaction
// artifact. Callers preserve frontier order; composition keeps each artifact
// separate so later restacks can supersede only the ranges they actually cover.
// AnchorEndMs is when the last source it covers was created: the summary
// renders right after that source, whether or not the source is loaded.
type CompactionArtifact struct {
	ID             string             `json:"id"`
	Summary        string             `json:"summary"`
	AnchorEndMs    int64              `json:"anchor_end_ms,omitempty"`
	CoverageAsOfMs int64              `json:"coverage_as_of_ms,omitempty"`
	Sources        []CompactionSource `json:"sources,omitempty"`
}

// ActiveRenderedContext removes only segments covered by usable artifacts.
func ActiveRenderedContext(rc RenderedContext, artifacts []CompactionArtifact) RenderedContext {
	return filterCoveredRenderedContext(rc, coveredExternalMessages(artifacts))
}

const earliestMergeTime int64 = -1 << 63

type mergeEntry struct {
	source *turn.ContextMessageSource
	kind   string // "rc", "summary", or "tr"
	time   int64
	step   int
	// artifact is one more than the index of the artifact that replaces
	// this entry, or zero when none does.
	artifact int
	// For RC entries
	rcContent []RenderedContentPiece
	// For summary entries
	summaryContent    string
	summaryArtifactID string
	// For TR entries
	trRole       string
	trContent    string
	trRawContent json.RawMessage
}

// MergeContext interleaves RC segments and TR entries by timestamp.
// RC entries use receivedAtMs; TR entries use requestedAtMs.
// Tiebreaker: RC before TR on equal timestamp.
// Each RC entry remains a separate user message so selection preserves its source boundary.
func MergeContext(rc RenderedContext, trs []TurnResponseEntry) []ContextMessage {
	entries := make([]mergeEntry, 0, len(rc)+len(trs))
	entries = appendRenderedContextEntries(entries, rc)
	entries = appendTurnResponseEntries(entries, trs)
	sortMergeEntries(entries)
	return mergeEntries(entries)
}

func appendRenderedContextEntries(entries []mergeEntry, rc RenderedContext) []mergeEntry {
	for i, seg := range rc {
		entries = append(entries, mergeEntry{
			kind:      "rc",
			time:      seg.ReceivedAtMs,
			step:      2 * i,
			rcContent: seg.Content,
			source:    renderedMessageSource(seg),
		})
	}
	return entries
}

func appendTurnResponseEntries(entries []mergeEntry, trs []TurnResponseEntry) []mergeEntry {
	for i, tr := range trs {
		entries = append(entries, mergeEntry{
			kind:         "tr",
			time:         tr.RequestedAtMs,
			step:         2 * i,
			trRole:       tr.Role,
			source:       &turn.ContextMessageSource{Kind: "history", ID: tr.SourceMessageID, ContentHash: tr.ContentHash},
			trContent:    tr.Content,
			trRawContent: tr.RawContent,
		})
	}
	return entries
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
		return 1
	case "summary":
		return 2
	case "tr":
		return 3
	default:
		return 4
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
				messages = append(messages, ContextMessage{Role: "user", Content: pendingText.String(), Source: entry.source})
			}
		case "summary":
			messages = append(messages, ContextMessage{
				Role:                 "user",
				Content:              entry.summaryContent,
				CompactionArtifactID: entry.summaryArtifactID,
			})
		case "tr":
			messages = append(messages, ContextMessage{
				Role:       entry.trRole,
				Source:     entry.source,
				Content:    entry.trContent,
				RawContent: entry.trRawContent,
			})
		}
	}

	return messages
}

// ComposeContext merges un-compacted RC and TR streams.
func ComposeContext(rc RenderedContext, trs []TurnResponseEntry) *ComposeContextResult {
	return ComposeContextWithArtifacts(rc, trs, nil)
}

// ComposeContextWithArtifacts replaces the RC/TR sources each active
// artifact covers with its summary, placed by turn.PlaceSummary; an artifact
// none of whose sources is loaded renders at its durable anchor.
func ComposeContextWithArtifacts(rc RenderedContext, trs []TurnResponseEntry, artifacts []CompactionArtifact) *ComposeContextResult {
	allMessages := mergeEntries(composeMergeEntries(rc, trs, artifacts))
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
func composeMergeEntries(rc RenderedContext, trs []TurnResponseEntry, artifacts []CompactionArtifact) []mergeEntry {
	byExternal, byHistory := map[string]int{}, map[string]int{}
	externalCoverage := make(map[string]externalMessageCoverage)
	for i, artifact := range artifacts {
		if !artifact.usable() {
			continue
		}
		for _, source := range artifact.Sources {
			if id := strings.TrimSpace(source.ExternalMessageID); id != "" && source.CreatedAtMs > 0 && byExternal[id] == 0 {
				byExternal[id] = i + 1
				externalCoverage[id] = sourceCoverage(source, artifact)
			}
			if id := strings.TrimSpace(source.HistoryMessageID); id != "" && byHistory[id] == 0 {
				byHistory[id] = i + 1
			}
		}
	}
	entries := make([]mergeEntry, 0, len(rc)+len(trs)+len(artifacts))
	entries = appendRenderedContextEntries(entries, rc)
	for i, segment := range rc {
		if a := byExternal[strings.TrimSpace(segment.MessageID)]; a > 0 &&
			renderedSegmentCovered(segment, externalCoverage[strings.TrimSpace(segment.MessageID)]) {
			entries[i].artifact = a
		}
	}
	entries = appendTurnResponseEntries(entries, trs)
	for i, tr := range trs {
		entries[len(rc)+i].artifact = byHistory[strings.TrimSpace(tr.SourceMessageID)]
	}
	// A summary renders right after the last source it covers. When that
	// source is not loaded, its durable anchor holds the place.
	for i, artifact := range artifacts {
		if artifact.usable() && !anchorSourceLoaded(entries, artifact, i) {
			anchor := summaryMergeEntry(artifact, i)
			anchor.artifact = i + 1
			entries = append(entries, anchor)
		}
	}
	sortMergeEntries(entries)
	for i, artifact := range artifacts {
		if artifact.usable() {
			entries, _ = turn.PlaceSummary(entries, summaryMergeEntry(artifact, i), func(entry mergeEntry) bool { return entry.artifact == i+1 })
		}
	}
	return entries
}

// anchorSourceLoaded reports whether the last source artifact covers is
// among the entries it replaces.
func anchorSourceLoaded(entries []mergeEntry, artifact CompactionArtifact, index int) bool {
	if len(artifact.Sources) == 0 {
		return false
	}
	last := artifact.Sources[len(artifact.Sources)-1]
	return slices.ContainsFunc(entries, func(entry mergeEntry) bool {
		if entry.artifact != index+1 || entry.source == nil {
			return false
		}
		return entry.kind == "rc" && entry.source.ID == strings.TrimSpace(last.ExternalMessageID) ||
			entry.kind == "tr" && entry.source.ID == strings.TrimSpace(last.HistoryMessageID)
	})
}

// summaryMergeEntry renders artifact at its durable anchor.
func summaryMergeEntry(artifact CompactionArtifact, index int) mergeEntry {
	at := artifact.AnchorEndMs
	if at <= 0 {
		at = earliestMergeTime
	}
	return mergeEntry{
		kind:              "summary",
		time:              at,
		step:              index,
		summaryContent:    "<summary>\n" + strings.TrimSpace(artifact.Summary) + "\n</summary>",
		summaryArtifactID: artifact.ID,
	}
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
	// ProtectedOverflow is set when the protected set alone (artifact
	// summaries plus the newest entry) exceeds the budget; the caller must
	// fail closed instead of materializing (CM-ADM-002).
	ProtectedOverflow bool
}

// ComposeContextWithArtifactsBudgeted composes like
// ComposeContextWithArtifacts but makes the admission decision on entry
// metadata before materializing anything. Over budget it keeps every artifact
// summary plus the newest contiguous window that fits, dropping older raw
// entries deterministically. When even the protected set does not fit it
// returns a nil result with ProtectedOverflow set.
func ComposeContextWithArtifactsBudgeted(
	rc RenderedContext,
	trs []TurnResponseEntry,
	artifacts []CompactionArtifact,
	budget ComposeBudget,
) (*ComposeContextResult, ComposeAdmission) {
	entries := composeMergeEntries(rc, trs, artifacts)
	if len(entries) == 0 {
		return nil, ComposeAdmission{}
	}
	markCurrentEntries(entries, rc, budget.After)

	admitted := make([]turn.AdmissionEntry, len(entries))
	for i := range entries {
		admitted[i] = turn.AdmissionEntry{
			Cost:   mergeEntryTokens(entries[i]),
			Source: entries[i].source,
			Pinned: entries[i].kind == "summary",
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

func filterCoveredRenderedContext(
	rc RenderedContext,
	coveredMessages map[string]externalMessageCoverage,
) RenderedContext {
	if len(coveredMessages) == 0 {
		return rc
	}
	filtered := make(RenderedContext, 0, len(rc))
	for _, segment := range rc {
		coverage, covered := coveredMessages[strings.TrimSpace(segment.MessageID)]
		if covered && renderedSegmentCovered(segment, coverage) {
			continue
		}
		filtered = append(filtered, segment)
	}
	return filtered
}

type externalMessageCoverage struct {
	coverageAsOfMs int64
	eventCursor    int64
}

func sourceCoverage(source CompactionSource, artifact CompactionArtifact) externalMessageCoverage {
	asOf := artifact.CoverageAsOfMs
	if source.CoverageAsOfMs != nil {
		asOf = *source.CoverageAsOfMs
	}
	return externalMessageCoverage{coverageAsOfMs: asOf, eventCursor: source.EventCursor}
}

func coveredExternalMessages(artifacts []CompactionArtifact) map[string]externalMessageCoverage {
	covered := make(map[string]externalMessageCoverage)
	for _, artifact := range artifacts {
		if !artifact.usable() {
			continue
		}
		for _, source := range artifact.Sources {
			id := strings.TrimSpace(source.ExternalMessageID)
			if id == "" || source.CreatedAtMs <= 0 {
				continue
			}
			coverage := covered[id]
			if candidate := sourceCoverage(source, artifact); candidate.eventCursor > coverage.eventCursor || candidate.eventCursor == coverage.eventCursor && candidate.coverageAsOfMs > coverage.coverageAsOfMs {
				coverage = candidate
			}
			covered[id] = coverage
		}
	}
	return covered
}

// renderedSegmentCovered reports whether coverage may replace the segment. A
// segment edited after the artifact completed carries content the summary has
// never seen and must stay visible.
func renderedSegmentCovered(segment RenderedSegment, coverage externalMessageCoverage) bool {
	if coverage.eventCursor > 0 {
		return segment.LastEventCursor > 0 && segment.LastEventCursor <= coverage.eventCursor
	}
	if segment.EditedAtMs <= 0 {
		return true
	}
	return coverage.coverageAsOfMs > 0 && segment.EditedAtMs <= coverage.coverageAsOfMs
}

func (artifact CompactionArtifact) usable() bool {
	return strings.TrimSpace(artifact.ID) != "" && strings.TrimSpace(artifact.Summary) != ""
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
