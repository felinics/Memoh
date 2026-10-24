package compaction

import (
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

type CompactPolicy string

const (
	CompactPolicyPreserveRecent      CompactPolicy = "preserve_recent"
	CompactPolicyPreserveToolClosure CompactPolicy = "preserve_tool_closure"
	CompactPolicyMustKeep            CompactPolicy = "must_keep"
	// CompactPolicyAnchor keeps a row of the running turn raw where it is
	// (its input, a steer) without ending what a pass folds around it.
	CompactPolicyAnchor CompactPolicy = "anchor"
)

// CompactionCandidate is the typed view of one uncompacted history row used
// during candidate selection. RawContent retains the raw row payload; Record
// carries the typed classifier output used for policies, tool-aware
// boundaries, and summarizer rendering.
type CompactionCandidate struct {
	ID           pgtype.UUID
	RawContent   []byte
	Record       historyfrag.HistoryRecord
	Policies     []CompactPolicy
	IsToolResult bool
	EventCursor  int64
}

func (c CompactionCandidate) HasPolicy(policy CompactPolicy) bool {
	for _, p := range c.Policies {
		if p == policy {
			return true
		}
	}
	return false
}

// toolExchangeGroups partitions items into tool-exchange groups: a maximal run
// starting at a non-result row that carries the tool-closure policy (an
// assistant tool-call row) followed by every immediately-adjacent tool-result
// row answering it. Rows outside any exchange are returned as singleton
// groups, so an exchange can never be split by ID-blind index slicing.
func toolExchangeGroups(items []CompactionCandidate) [][]int {
	groups := make([][]int, 0, len(items))
	for i := 0; i < len(items); {
		if isToolResultItem(items[i]) || !items[i].HasPolicy(CompactPolicyPreserveToolClosure) {
			groups = append(groups, []int{i})
			i++
			continue
		}
		group := []int{i}
		j := i + 1
		for j < len(items) && isToolResultItem(items[j]) {
			group = append(group, j)
			j++
		}
		groups = append(groups, group)
		i = j
	}
	return groups
}

// protectPendingUserInput keeps an ask_user exchange in raw history while any
// of its calls has no answer: the continuation still needs the open call. An
// answered exchange is ordinary history the summary can cover.
func protectPendingUserInput(items []CompactionCandidate) {
	for _, group := range toolExchangeGroups(items) {
		if isToolResultItem(items[group[0]]) {
			continue
		}
		pending := askUserCallIDs(items[group[0]].Record)
		for _, idx := range group[1:] {
			mm := items[idx].Record.ModelMessage
			delete(pending, strings.TrimSpace(mm.ToolCallID))
			for _, p := range parseEntryParts(mm.Content) {
				delete(pending, strings.TrimSpace(p.ToolCallID))
			}
		}
		if len(pending) == 0 {
			continue
		}
		for _, idx := range group {
			items[idx].Policies = appendPolicy(items[idx].Policies, CompactPolicyMustKeep)
		}
	}
}

func askUserCallIDs(record historyfrag.HistoryRecord) map[string]struct{} {
	ids := map[string]struct{}{}
	for _, call := range record.ModelMessage.ToolCalls {
		if isAskUserToolName(call.Function.Name) {
			ids[strings.TrimSpace(call.ID)] = struct{}{}
		}
	}
	for _, p := range parseEntryParts(record.ModelMessage.Content) {
		if isToolCallPartType(p.Type) && isAskUserToolName(p.ToolName) {
			ids[strings.TrimSpace(p.ToolCallID)] = struct{}{}
		}
	}
	return ids
}

// propagateMustKeepAcrossToolExchanges spreads CompactPolicyMustKeep across an
// entire tool exchange: if the call row or any of its answering tool-result
// rows is must-keep, every row in that exchange becomes must-keep, so
// excluding only the must-keep row can never orphan a sibling tool call/result.
func propagateMustKeepAcrossToolExchanges(items []CompactionCandidate) {
	for _, group := range toolExchangeGroups(items) {
		if len(group) < 2 {
			continue
		}
		mustKeep := false
		for _, idx := range group {
			if items[idx].HasPolicy(CompactPolicyMustKeep) {
				mustKeep = true
				break
			}
		}
		if !mustKeep {
			continue
		}
		for _, idx := range group {
			items[idx].Policies = appendPolicy(items[idx].Policies, CompactPolicyMustKeep)
		}
	}
}

func candidatePolicies(record historyfrag.HistoryRecord) []CompactPolicy {
	var policies []CompactPolicy
	if isToolExchangeRecord(record) {
		policies = appendPolicy(policies, CompactPolicyPreserveToolClosure)
	}
	if isAskUserRecord(record) {
		policies = appendPolicy(policies, CompactPolicyPreserveToolClosure)
	}
	return policies
}

func appendPolicy(policies []CompactPolicy, policy CompactPolicy) []CompactPolicy {
	for _, p := range policies {
		if p == policy {
			return policies
		}
	}
	return append(policies, policy)
}

func isToolExchangeRecord(record historyfrag.HistoryRecord) bool {
	mm := record.ModelMessage
	if strings.EqualFold(strings.TrimSpace(mm.Role), "tool") {
		return true
	}
	if len(mm.ToolCalls) > 0 {
		return true
	}
	for _, p := range parseEntryParts(mm.Content) {
		if isToolPartType(p.Type) {
			return true
		}
	}
	return false
}

func isAskUserRecord(record historyfrag.HistoryRecord) bool {
	mm := record.ModelMessage
	if isAskUserToolName(mm.Name) {
		return true
	}
	for _, call := range mm.ToolCalls {
		if isAskUserToolName(call.Function.Name) {
			return true
		}
	}
	for _, p := range parseEntryParts(mm.Content) {
		if isAskUserToolName(p.ToolName) {
			return true
		}
	}
	return false
}

func isAskUserToolName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), userinput.ToolNameAskUser)
}

// estimateBytesAsTokens prices text the way every provider request is
// priced, so a summarizer call is sized for the window it is sent to.
func estimateBytesAsTokens(value string) int {
	return contextfrag.ProviderBudgetTokensFromBytes(len(value))
}

// foldableItems is the history before the first row a compaction must keep,
// extended over the tool results that answer a call it folds. A must-keep row
// ends it too: no summary covers rows on both sides of one.
func foldableItems(items []CompactionCandidate) []CompactionCandidate {
	protectedStart := firstPolicyStart(items, CompactPolicyPreserveRecent)
	cutoff := adjustForToolBoundary(items, protectedStart)
	if cutoff <= 0 || cutoff > protectedStart {
		return nil
	}
	return items[:min(cutoff, firstPolicyStart(items, CompactPolicyMustKeep))]
}

func firstPolicyStart(items []CompactionCandidate, policy CompactPolicy) int {
	for i, item := range items {
		if item.HasPolicy(policy) {
			return i
		}
	}
	return len(items)
}

// adjustForToolBoundary moves the compact/keep cutoff forward so the kept
// (newest) side never begins with an orphan tool result whose tool call is
// being compacted. Tool results are pulled into the compact set so each tool
// exchange stays intact on one side of the boundary.
func adjustForToolBoundary(items []CompactionCandidate, cutoff int) int {
	for cutoff > 0 && cutoff < len(items) && isToolClosureResultItem(items[cutoff]) {
		cutoff++
	}
	return cutoff
}

func isToolClosureResultItem(item CompactionCandidate) bool {
	return item.HasPolicy(CompactPolicyPreserveToolClosure) && isToolResultItem(item)
}

func isToolResultItem(item CompactionCandidate) bool {
	return item.IsToolResult
}

// buildEntriesAndIDs renders the summarizer entries and the ids to mark
// compacted from foldable items. An anchor renders in place as a kept entry
// and is not marked. A row that renders nothing (a reasoning-only message,
// an empty tool result) is covered with its neighbours and contributes no
// entry: leaving it raw would split the span into fragments too short to
// summarize.
func buildEntriesAndIDs(items []CompactionCandidate) ([]messageEntry, []pgtype.UUID) {
	var entries []messageEntry
	var ids []pgtype.UUID
	for _, item := range items {
		if !item.HasPolicy(CompactPolicyAnchor) {
			ids = append(ids, item.ID)
		}
		if entry, ok := candidateEntry(item); ok {
			entries = append(entries, entry)
		}
	}
	if len(coveredEntries(entries)) == 0 {
		return nil, nil
	}
	return entries, ids
}

// candidateEntry is what the summarizer reads for item, kept when item is an
// anchor. A row that renders nothing has no entry.
func candidateEntry(item CompactionCandidate) (messageEntry, bool) {
	content := renderCandidateEntry(item.Record)
	if strings.TrimSpace(content) == "" {
		return messageEntry{}, false
	}
	return messageEntry{Role: item.Record.ModelMessage.Role, Content: content, Kept: item.HasPolicy(CompactPolicyAnchor)}, true
}

// keptAfterSummary splits off the kept entries after the last one a summary
// covers. The request keeps them after the summary, so the summarizer reads
// them with what follows it rather than in place.
func keptAfterSummary(entries []messageEntry) ([]messageEntry, []messageEntry) {
	last := len(entries)
	for last > 0 && entries[last-1].Kept {
		last--
	}
	after := make([]messageEntry, 0, len(entries)-last)
	for _, entry := range entries[last:] {
		entry.Kept = false
		after = append(after, entry)
	}
	return entries[:last], after
}

// keptInPlace are the kept entries a summary is written around.
func keptInPlace(entries []messageEntry) []messageEntry {
	var kept []messageEntry
	for _, entry := range entries {
		if entry.Kept {
			kept = append(kept, entry)
		}
	}
	return kept
}

// coveredEntries are the entries a summary replaces: all but the kept ones.
func coveredEntries(entries []messageEntry) []messageEntry {
	var covered []messageEntry
	for _, entry := range entries {
		if !entry.Kept {
			covered = append(covered, entry)
		}
	}
	return covered
}

// trimCompactMessages keeps the oldest tool-exchange groups whose rendered
// entries fit maxTokens, deferring the rest to a later pass. Chewing
// front-to-back reclaims the oldest raw rows first and keeps summary coverage
// in chronological order. Must-keep groups and rows that render nothing cost
// nothing, exactly as buildEntriesAndIDs feeds the summarizer; group-aligned
// cuts never split a tool exchange. A first group that does not fit leaves
// nothing: its content is never cut to make it fit.
func trimCompactMessages(items []CompactionCandidate, maxTokens int) []CompactionCandidate {
	accumulated, end := 0, 0
	for _, group := range toolExchangeGroups(items) {
		cost := markableGroupCost(items, group)
		if accumulated+cost > maxTokens {
			break
		}
		accumulated += cost
		end = group[len(group)-1] + 1
	}
	return items[:end]
}

// leadingGroupCost is what trimCompactMessages must admit before any entry
// reaches the summarizer: the cost of the first group that renders one.
func leadingGroupCost(items []CompactionCandidate) int {
	for _, group := range toolExchangeGroups(items) {
		if cost := markableGroupCost(items, group); cost > 0 {
			return cost
		}
	}
	return 0
}

// markableGroupCost is the summarizer-prompt cost of one tool-exchange group:
// zero for a must-keep group, otherwise the line buildUserPrompt emits for
// each rendered entry, kept ones in their wrapper, as entriesPromptCost
// prices it, so tiny-but-real entries can never ride along for free. Rows
// that render nothing cost nothing. Costing the rendered bytes rather than
// the raw usage estimate matters: a row whose generation cost five tokens
// can still render a two-kilobyte tool outcome into the prompt.
func markableGroupCost(items []CompactionCandidate, group []int) int {
	cost := 0
	for _, idx := range group {
		if items[idx].HasPolicy(CompactPolicyMustKeep) {
			return 0
		}
		if entry, ok := candidateEntry(items[idx]); ok {
			cost += entryPromptCost(entry)
		}
	}
	return cost
}
