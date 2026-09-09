package builtin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/memory/migrate"
)

const compactMaxCandidateChars = 24000

// ---- Runtime: compact ----

// Compact performs a graph-native cleanup pass without requiring an LLM. It
// canonicalizes node IDs and merges exact duplicate concept bodies, but avoids
// semantic rewrites when no LLM is available.
func (r *graphRuntime) Compact(ctx context.Context, filters map[string]any, ratio float64, _ int) (adapters.CompactResult, error) {
	return r.compactConcepts(ctx, filters, ratio, 0, nil)
}

// CompactWithLLM runs graph-native semantic compaction. Unlike the legacy file
// compactor, graph compaction does not clear the node table and mint fresh IDs:
// it canonicalizes node IDs, merges nodes that describe the same concept, keeps
// the representative ID stable, and records source lineage on the merged node.
func (r *graphRuntime) CompactWithLLM(ctx context.Context, filters map[string]any, ratio float64, decayDays int, llm adapters.LLM) (adapters.CompactResult, error) {
	if llm == nil {
		return adapters.CompactResult{}, errors.New("graph runtime: llm compact requires an LLM")
	}
	return r.compactConcepts(ctx, filters, ratio, decayDays, llm)
}

func (r *graphRuntime) compactConcepts(ctx context.Context, filters map[string]any, ratio float64, decayDays int, llm adapters.LLM) (adapters.CompactResult, error) {
	if r.store == nil {
		return adapters.CompactResult{}, errors.New("graph runtime: wiki store not configured")
	}
	botID, err := runtimeBotID("", filters)
	if err != nil {
		return adapters.CompactResult{}, err
	}
	if ratio <= 0 || ratio > 1 {
		return adapters.CompactResult{}, errors.New("ratio must be in range (0, 1]")
	}
	nodes, err := r.store.ListNodes(ctx, botID)
	if err != nil {
		return adapters.CompactResult{}, fmt.Errorf("graph runtime: list nodes: %w", err)
	}
	before := len(nodes)
	if before == 0 {
		return adapters.CompactResult{BeforeCount: 0, AfterCount: 0, Ratio: ratio, Results: []adapters.MemoryItem{}}, nil
	}

	nodes, changed, err := r.normalizeCompactNodeIDs(ctx, botID, nodes)
	if err != nil {
		return adapters.CompactResult{}, err
	}

	deleteIDs := make([]string, 0)
	now := time.Now().UTC()
	for _, conceptNodes := range groupCompactConcepts(botID, nodes) {
		_, compactable := splitCompactProtectedNodes(conceptNodes)
		if len(compactable) < 2 {
			continue
		}
		if llm == nil && !compactSameBody(compactable) {
			continue
		}
		_, superseded, err := r.mergeCompactConcept(ctx, botID, compactable, ratio, decayDays, llm, now)
		if err != nil {
			return adapters.CompactResult{}, err
		}
		deleteIDs = append(deleteIDs, superseded...)
		changed = true
	}

	for _, id := range uniqueCompactStrings(deleteIDs) {
		if err := r.store.DeleteNode(ctx, botID, id); err != nil {
			return adapters.CompactResult{}, fmt.Errorf("graph runtime: compact delete superseded node: %w", err)
		}
	}
	r.retry.discard(botID, deleteIDs)
	r.discardSemanticNodes(ctx, botID, deleteIDs)
	if changed {
		r.syncAndInvalidate(ctx, botID)
	}

	keptNodes, err := r.store.ListNodes(ctx, botID)
	if err != nil {
		return adapters.CompactResult{}, fmt.Errorf("graph runtime: compact list after merge: %w", err)
	}
	sort.Slice(keptNodes, func(i, j int) bool { return keptNodes[i].CapturedAt.After(keptNodes[j].CapturedAt) })
	items := make([]adapters.MemoryItem, 0, len(keptNodes))
	for _, n := range keptNodes {
		items = append(items, nodeSpecToMemoryItem(n))
	}
	return adapters.CompactResult{BeforeCount: before, AfterCount: len(keptNodes), Ratio: ratio, Results: items}, nil
}

// ---- Canonical IDs ----

func (r *graphRuntime) normalizeCompactNodeIDs(ctx context.Context, botID string, nodes []migrate.NodeSpec) ([]migrate.NodeSpec, bool, error) {
	type bucket struct {
		canonicalID string
		nodes       []migrate.NodeSpec
		storedIDs   []string
	}
	buckets := make(map[string]*bucket, len(nodes))
	for _, node := range nodes {
		storedID := strings.TrimSpace(node.ID)
		canonicalID := compactCanonicalMemoryID(botID, storedID)
		if canonicalID == "" {
			continue
		}
		node.ID = canonicalID
		node.BotID = botID
		if node.Hash == "" && strings.TrimSpace(node.Body) != "" {
			node.Hash = runtimeHash(node.Body)
		}
		b := buckets[canonicalID]
		if b == nil {
			b = &bucket{canonicalID: canonicalID}
			buckets[canonicalID] = b
		}
		b.nodes = append(b.nodes, node)
		b.storedIDs = append(b.storedIDs, storedID)
	}

	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	changed := false
	normalized := make([]migrate.NodeSpec, 0, len(keys))
	deletedIDs := make([]string, 0)
	for _, key := range keys {
		b := buckets[key]
		sortCompactNodes(b.nodes)
		best := completeCompactNode(b.nodes[0], b.nodes)
		best.ID = b.canonicalID
		best.BotID = botID
		needsUpsert := false
		for _, storedID := range uniqueCompactStrings(b.storedIDs) {
			if storedID == "" || storedID == b.canonicalID {
				continue
			}
			deletedIDs = append(deletedIDs, storedID)
			needsUpsert = true
			changed = true
		}
		if needsUpsert {
			saved, err := r.store.UpsertNode(ctx, best)
			if err != nil {
				return nil, false, fmt.Errorf("graph runtime: compact canonicalize node: %w", err)
			}
			best = saved
			r.semanticUpsertBestEffort(botID, saved) //nolint:contextcheck // async semantic upsert uses its own bounded context
		}
		normalized = append(normalized, best)
	}

	for _, id := range uniqueCompactStrings(deletedIDs) {
		if err := r.store.DeleteNode(ctx, botID, id); err != nil {
			return nil, false, fmt.Errorf("graph runtime: compact delete legacy node: %w", err)
		}
	}
	r.retry.discard(botID, deletedIDs)
	r.discardSemanticNodes(ctx, botID, deletedIDs)
	return normalized, changed, nil
}

func compactCanonicalMemoryID(botID, memoryID string) string {
	localID := runtimeLocalMemoryID(memoryID)
	if localID == "" {
		return ""
	}
	return strings.TrimSpace(botID) + ":" + localID
}

// ---- Concept Merge ----

func (r *graphRuntime) mergeCompactConcept(ctx context.Context, botID string, nodes []migrate.NodeSpec, ratio float64, decayDays int, llm adapters.LLM, now time.Time) (migrate.NodeSpec, []string, error) {
	sortCompactNodes(nodes)
	representative := completeCompactNode(nodes[0], nodes)
	body := strings.TrimSpace(representative.Body)
	if llm != nil {
		candidates := compactCandidates(nodes)
		if len(candidates) > 0 {
			resp, err := llm.Compact(ctx, adapters.CompactRequest{
				BotID:       botID,
				Memories:    candidates,
				TargetCount: 1,
				DecayDays:   decayDays,
			})
			if err != nil {
				return migrate.NodeSpec{}, nil, fmt.Errorf("graph runtime: llm compact concept: %w", err)
			}
			if len(resp.Facts) > 0 {
				if fact := strings.TrimSpace(resp.Facts[0]); fact != "" {
					body = fact
				}
			}
		}
	}
	if body == "" {
		body = strings.TrimSpace(representative.Body)
	}
	representative.Body = body
	representative.Hash = runtimeHash(body)
	representative.CapturedAt = now
	representative.Metadata = compactMetadata(representative.Metadata, nodes, representative.ID, ratio, now)

	superseded := make([]string, 0, len(nodes)-1)
	for _, node := range nodes {
		if node.ID == representative.ID {
			continue
		}
		superseded = append(superseded, compactCanonicalMemoryID(botID, node.ID))
	}
	saved, err := r.store.UpsertNode(ctx, representative)
	if err != nil {
		return migrate.NodeSpec{}, nil, fmt.Errorf("graph runtime: compact upsert merged concept: %w", err)
	}
	r.semanticUpsertBestEffort(botID, saved) //nolint:contextcheck // async semantic upsert uses its own bounded context
	return saved, uniqueCompactStrings(superseded), nil
}

func groupCompactConcepts(botID string, nodes []migrate.NodeSpec) [][]migrate.NodeSpec {
	groups := make(map[string][]migrate.NodeSpec, len(nodes))
	for _, node := range nodes {
		key := compactConceptKey(botID, node)
		groups[key] = append(groups[key], node)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	concepts := make([][]migrate.NodeSpec, 0, len(keys))
	for _, key := range keys {
		nodes := groups[key]
		sortCompactNodes(nodes)
		concepts = append(concepts, nodes)
	}
	return concepts
}

func compactConceptKey(botID string, node migrate.NodeSpec) string {
	profile := strings.ToLower(strings.TrimSpace(node.ProfileRef))
	if profile == "" {
		profile = strings.ToLower(metadataStringVal(node.Metadata, "profile_ref"))
	}
	subjectSlug := migrate.NodeSlug("", node.Subject, "")
	if subjectSlug == "" {
		subjectSlug = migrate.NodeSlug("", metadataStringVal(node.Metadata, "subject"), "")
	}
	topicSlug := migrate.NodeSlug("", "", node.Topic)
	if topicSlug == "" {
		topicSlug = migrate.NodeSlug("", "", metadataStringVal(node.Metadata, "topic"))
	}
	if subjectSlug != "" {
		return "concept:" + profile + ":" + subjectSlug
	}
	if topicSlug != "" {
		return "topic:" + profile + ":" + topicSlug
	}
	if normalizedBody := normalizeCompactBody(node.Body); normalizedBody != "" {
		return "body:" + runtimeHash(normalizedBody)
	}
	return "id:" + compactCanonicalMemoryID(botID, node.ID)
}

func splitCompactProtectedNodes(nodes []migrate.NodeSpec) ([]migrate.NodeSpec, []migrate.NodeSpec) {
	protected := make([]migrate.NodeSpec, 0)
	compactable := make([]migrate.NodeSpec, 0, len(nodes))
	for _, node := range nodes {
		if compactProtectedNode(node) {
			protected = append(protected, node)
		} else {
			compactable = append(compactable, node)
		}
	}
	return protected, compactable
}

func compactProtectedNode(node migrate.NodeSpec) bool {
	return compactMetadataTruthy(node.Metadata, "pinned") || compactMetadataTruthy(node.Metadata, "read_only")
}

func compactMetadataTruthy(metadata map[string]any, key string) bool {
	value, ok := metadata[key]
	if !ok {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "y", "on":
			return true
		default:
			return false
		}
	default:
		return strings.EqualFold(strings.TrimSpace(fmt.Sprint(v)), "true")
	}
}

func compactSameBody(nodes []migrate.NodeSpec) bool {
	if len(nodes) < 2 {
		return true
	}
	first := normalizeCompactBody(nodes[0].Body)
	if first == "" {
		return false
	}
	for _, node := range nodes[1:] {
		if normalizeCompactBody(node.Body) != first {
			return false
		}
	}
	return true
}

func compactCandidates(nodes []migrate.NodeSpec) []adapters.CandidateMemory {
	candidates := make([]adapters.CandidateMemory, 0, len(nodes))
	for _, node := range nodes {
		body := strings.TrimSpace(node.Body)
		if body == "" {
			continue
		}
		candidates = append(candidates, adapters.CandidateMemory{
			ID:        node.ID,
			Memory:    body,
			CreatedAt: formatNodeTime(node.CapturedAt),
			Metadata:  buildNodeMetadata(node),
		})
	}
	for compactCandidateChars(candidates) > compactMaxCandidateChars && len(candidates) > 1 {
		candidates = candidates[:len(candidates)-1]
	}
	return candidates
}

func compactCandidateChars(candidates []adapters.CandidateMemory) int {
	total := 0
	for _, candidate := range candidates {
		total += len(candidate.ID) + len(candidate.Memory) + len(candidate.CreatedAt) + 32
		for key, value := range candidate.Metadata {
			total += len(key) + len(fmt.Sprint(value)) + 8
		}
	}
	return total
}

// ---- Provenance Metadata ----

func compactMetadata(metadata map[string]any, nodes []migrate.NodeSpec, representativeID string, ratio float64, compactedAt time.Time) map[string]any {
	out := cloneCompactMetadata(metadata)
	sourceIDs := compactSourceIDs(nodes)
	supersededIDs := make([]string, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		if id != representativeID {
			supersededIDs = append(supersededIDs, id)
		}
	}
	out["compacted_at"] = compactedAt.UTC().Format(time.RFC3339)
	out["compaction_strategy"] = "concept_merge"
	out["compaction_ratio"] = ratio
	out["compaction_source_ids"] = sourceIDs
	out["compaction_source_count"] = len(sourceIDs)
	if len(supersededIDs) > 0 {
		out["superseded_ids"] = supersededIDs
	}
	return out
}

func compactSourceIDs(nodes []migrate.NodeSpec) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		botID := strings.TrimSpace(node.BotID)
		if id := compactCanonicalMemoryID(botID, node.ID); id != "" {
			ids = append(ids, id)
		}
		for _, id := range compactMetadataStringList(node.Metadata, "compaction_source_ids") {
			if canonical := compactCanonicalMemoryID(botID, id); canonical != "" {
				ids = append(ids, canonical)
			}
		}
	}
	return uniqueCompactStrings(ids)
}

func compactMetadataStringList(metadata map[string]any, key string) []string {
	if metadata == nil {
		return nil
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return nil
	}
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return []string{s}
		}
	}
	return nil
}

// ---- Node Completion ----

func completeCompactNode(node migrate.NodeSpec, sources []migrate.NodeSpec) migrate.NodeSpec {
	if node.Layer == "" {
		node.Layer = migrate.LayerNote
	}
	if node.BotID == "" && len(sources) > 0 {
		node.BotID = sources[0].BotID
	}
	if node.Hash == "" && strings.TrimSpace(node.Body) != "" {
		node.Hash = runtimeHash(node.Body)
	}
	if node.CapturedAt.IsZero() {
		node.CapturedAt = time.Now().UTC()
	}
	for _, source := range sources {
		if node.Subject == "" {
			node.Subject = source.Subject
		}
		if node.Topic == "" {
			node.Topic = source.Topic
		}
		if node.ProfileRef == "" {
			node.ProfileRef = source.ProfileRef
		}
		if node.FactType == "" {
			node.FactType = source.FactType
		}
		if node.Confidence == 0 && source.Confidence != 0 {
			node.Confidence = source.Confidence
		}
	}
	if node.Confidence == 0 {
		node.Confidence = 0.5
	}
	node.Metadata = cloneCompactMetadata(node.Metadata)
	node.SourceMessageIDs = mergeCompactSourceMessageIDs(sources)
	return node
}

func mergeCompactSourceMessageIDs(nodes []migrate.NodeSpec) []string {
	ordered := append([]migrate.NodeSpec(nil), nodes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].CapturedAt.Equal(ordered[j].CapturedAt) {
			return ordered[i].CapturedAt.Before(ordered[j].CapturedAt)
		}
		return ordered[i].ID < ordered[j].ID
	})
	values := make([]string, 0)
	for _, node := range ordered {
		values = append(values, node.SourceMessageIDs...)
	}
	return adapters.NormalizeSourceRefs(values)
}

func cloneCompactMetadata(metadata map[string]any) map[string]any {
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

// ---- Ordering / Small Utilities ----

func sortCompactNodes(nodes []migrate.NodeSpec) {
	sort.SliceStable(nodes, func(i, j int) bool {
		leftRank := compactNodeRank(nodes[i])
		rightRank := compactNodeRank(nodes[j])
		if leftRank != rightRank {
			return leftRank > rightRank
		}
		if !nodes[i].CapturedAt.Equal(nodes[j].CapturedAt) {
			return nodes[i].CapturedAt.After(nodes[j].CapturedAt)
		}
		return nodes[i].ID < nodes[j].ID
	})
}

func compactNodeRank(node migrate.NodeSpec) int {
	rank := len(strings.TrimSpace(node.Body))
	if node.Subject != "" {
		rank += 200
	}
	if node.Topic != "" {
		rank += 100
	}
	if node.ProfileRef != "" {
		rank += 50
	}
	rank += len(node.Metadata) * 10
	return rank
}

func normalizeCompactBody(body string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(body))), " ")
}

func uniqueCompactStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
