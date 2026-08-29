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
}

// ContextMessage is a unified message for LLM context, produced by MergeContext.
type ContextMessage struct {
	Role                 string          `json:"role"`
	Content              string          `json:"content"`
	RawContent           json.RawMessage `json:"raw_content,omitempty"`
	CompactionArtifactID string          `json:"compaction_artifact_id,omitempty"`
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
}

// CompactionArtifact is the timeline-facing projection of one active compaction
// artifact. Callers preserve frontier order; composition keeps each artifact
// separate so later restacks can supersede only the ranges they actually cover.
type CompactionArtifact struct {
	ID             string             `json:"id"`
	Summary        string             `json:"summary"`
	AnchorStartMs  int64              `json:"anchor_start_ms,omitempty"`
	CoverageAsOfMs int64              `json:"coverage_as_of_ms,omitempty"`
	Sources        []CompactionSource `json:"sources,omitempty"`
}

// ActiveRenderedContext removes only segments covered by usable artifacts.
func ActiveRenderedContext(rc RenderedContext, artifacts []CompactionArtifact) RenderedContext {
	return filterCoveredRenderedContext(rc, coveredExternalMessages(artifacts))
}

const earliestMergeTime int64 = -1 << 63

type mergeEntry struct {
	kind string // "summary_before_rc", "rc", "summary", or "tr"
	time int64
	step int
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
// Consecutive RC entries between TR entries are merged into one user message.
func MergeContext(rc RenderedContext, trs []TurnResponseEntry) []ContextMessage {
	entries := make([]mergeEntry, 0, len(rc)+len(trs))
	entries = appendRenderedContextEntries(entries, rc)
	entries = appendTurnResponseEntries(entries, trs)
	return mergeEntries(entries)
}

func appendRenderedContextEntries(entries []mergeEntry, rc RenderedContext) []mergeEntry {
	for i, seg := range rc {
		entries = append(entries, mergeEntry{
			kind:      "rc",
			time:      seg.ReceivedAtMs,
			step:      2 * i,
			rcContent: seg.Content,
		})
	}
	return entries
}

// appendActiveRenderedContextEntries appends uncovered segments keyed by their
// position in the full rendered context, so slot summaries and surviving
// segments share one index space.
func appendActiveRenderedContextEntries(
	entries []mergeEntry,
	rc RenderedContext,
	coveredMessages map[string]externalMessageCoverage,
) []mergeEntry {
	for i, segment := range rc {
		coverage, covered := coveredMessages[strings.TrimSpace(segment.MessageID)]
		if covered && renderedSegmentCovered(segment, coverage) {
			continue
		}
		entries = append(entries, mergeEntry{
			kind:      "rc",
			time:      segment.ReceivedAtMs,
			step:      2 * i,
			rcContent: segment.Content,
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
			trContent:    tr.Content,
			trRawContent: tr.RawContent,
		})
	}
	return entries
}

// appendActiveTurnResponseEntries appends uncovered responses keyed by their
// position in the full response list, so slot summaries and surviving
// responses share one index space.
func appendActiveTurnResponseEntries(entries []mergeEntry, trs []TurnResponseEntry, coveredIDs map[string]struct{}) []mergeEntry {
	for i, tr := range trs {
		if _, covered := coveredIDs[strings.TrimSpace(tr.SourceMessageID)]; covered {
			continue
		}
		entries = append(entries, mergeEntry{
			kind:         "tr",
			time:         tr.RequestedAtMs,
			step:         2 * i,
			trRole:       tr.Role,
			trContent:    tr.Content,
			trRawContent: tr.RawContent,
		})
	}
	return entries
}

func mergeEntries(entries []mergeEntry) []ContextMessage {
	sortMergeEntries(entries)
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
	case "rc", "summary_slot", "summary_before_rc":
		return 1
	case "summary":
		return 2
	case "tr", "summary_tr_slot":
		return 3
	default:
		return 4
	}
}

func materializeMergeEntries(entries []mergeEntry) []ContextMessage {
	var messages []ContextMessage
	var pendingText strings.Builder

	flushRC := func() {
		if pendingText.Len() > 0 {
			messages = append(messages, ContextMessage{Role: "user", Content: pendingText.String()})
			pendingText.Reset()
		}
	}

	for _, entry := range entries {
		switch entry.kind {
		case "rc":
			for _, piece := range entry.rcContent {
				if piece.Type == "text" {
					if pendingText.Len() > 0 {
						pendingText.WriteByte('\n')
					}
					pendingText.WriteString(piece.Text)
				}
			}
		case "summary", "summary_slot", "summary_before_rc", "summary_tr_slot":
			flushRC()
			messages = append(messages, ContextMessage{
				Role:                 "user",
				Content:              entry.summaryContent,
				CompactionArtifactID: entry.summaryArtifactID,
			})
		case "tr":
			flushRC()
			messages = append(messages, ContextMessage{
				Role:       entry.trRole,
				Content:    entry.trContent,
				RawContent: entry.trRawContent,
			})
		}
	}
	flushRC()

	return messages
}

// ComposeContext merges un-compacted RC and TR streams.
func ComposeContext(rc RenderedContext, trs []TurnResponseEntry) *ComposeContextResult {
	return ComposeContextWithArtifacts(rc, trs, nil)
}

// ComposeContextWithArtifacts replaces covered RC/TR sources with each active
// artifact at the covered rendered slot when available, or its durable anchor.
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

// composeMergeEntries builds the unsorted merge-entry list for composition.
// Entries hold references into rc/trs plus small summary strings; nothing
// large is materialized here, so admission can measure before it selects.
func composeMergeEntries(rc RenderedContext, trs []TurnResponseEntry, artifacts []CompactionArtifact) []mergeEntry {
	coveredMessages := coveredExternalMessages(artifacts)
	coveredResponseIDs := coveredHistoryMessageIDs(artifacts)
	entries := make([]mergeEntry, 0, len(rc)+len(trs)+len(artifacts))
	entries = appendActiveRenderedContextEntries(entries, rc, coveredMessages)
	for i, artifact := range artifacts {
		if !artifact.usable() {
			continue
		}
		summaryAtMs, kind, step := artifactSummaryPlacement(artifact, rc, coveredMessages, trs, i)
		entries = append(entries, mergeEntry{
			kind:              kind,
			time:              summaryAtMs,
			step:              step,
			summaryContent:    "<summary>\n" + strings.TrimSpace(artifact.Summary) + "\n</summary>",
			summaryArtifactID: artifact.ID,
		})
	}
	entries = appendActiveTurnResponseEntries(entries, trs, coveredResponseIDs)
	return entries
}

// ComposeBudget bounds composition before materialization (CM-ADM-001).
type ComposeBudget struct {
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
	sortMergeEntries(entries)

	admitted := make([]turn.AdmissionEntry, len(entries))
	for i := range entries {
		admitted[i] = turn.AdmissionEntry{
			Cost:   mergeEntryTokens(entries[i]),
			Pinned: isSummaryMergeKind(entries[i].kind),
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
	if decision.ProtectedOverflow {
		return nil, admission
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

func isSummaryMergeKind(kind string) bool {
	switch kind {
	case "summary", "summary_slot", "summary_before_rc", "summary_tr_slot":
		return true
	default:
		return false
	}
}

// mergeEntryTokens estimates one entry's cost from lengths alone, without
// materializing the entry.
func mergeEntryTokens(entry mergeEntry) int {
	switch entry.kind {
	case "rc":
		n := 0
		for _, piece := range entry.rcContent {
			if piece.Type == "text" {
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

func artifactSummaryPlacement(
	artifact CompactionArtifact,
	rc RenderedContext,
	coveredMessages map[string]externalMessageCoverage,
	trs []TurnResponseEntry,
	artifactIndex int,
) (int64, string, int) {
	for _, source := range artifact.Sources {
		if source.CreatedAtMs <= 0 {
			continue
		}
		id := strings.TrimSpace(source.ExternalMessageID)
		if id == "" {
			break
		}
		coverage, covered := coveredMessages[id]
		if !covered {
			break
		}
		for i, segment := range rc {
			if strings.TrimSpace(segment.MessageID) == id {
				if renderedSegmentCovered(segment, coverage) {
					return segment.ReceivedAtMs, "summary_slot", 2 * i
				}
				return segment.ReceivedAtMs, "summary_before_rc", 2*i - 1
			}
		}
		break
	}
	for _, source := range artifact.Sources {
		id := strings.TrimSpace(source.HistoryMessageID)
		if id == "" {
			continue
		}
		for i, tr := range trs {
			if strings.TrimSpace(tr.SourceMessageID) == id {
				return tr.RequestedAtMs, "summary_tr_slot", 2 * i
			}
		}
	}
	if artifact.AnchorStartMs <= 0 {
		return earliestMergeTime, "summary", artifactIndex
	}
	return artifact.AnchorStartMs, "summary", artifactIndex
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

func coveredHistoryMessageIDs(artifacts []CompactionArtifact) map[string]struct{} {
	covered := make(map[string]struct{})
	for _, artifact := range artifacts {
		if !artifact.usable() {
			continue
		}
		for _, source := range artifact.Sources {
			if id := strings.TrimSpace(source.HistoryMessageID); id != "" {
				covered[id] = struct{}{}
			}
		}
	}
	return covered
}

type externalMessageCoverage struct {
	coverageAsOfMs int64
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
			if artifact.CoverageAsOfMs > coverage.coverageAsOfMs {
				coverage.coverageAsOfMs = artifact.CoverageAsOfMs
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
