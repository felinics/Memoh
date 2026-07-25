package memory

import (
	"sort"
	"strings"
	"time"
)

// GraphNode is one concept node in the memory graph visualization.
type GraphNode struct {
	ID        string         `json:"id"`
	Label     string         `json:"label"`
	Slug      string         `json:"slug"`
	Memory    string         `json:"memory"`
	Subject   string         `json:"subject,omitempty"`
	Topic     string         `json:"topic,omitempty"`
	Count     int            `json:"count,omitempty"`
	MemoryIDs []string       `json:"memory_ids,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// GraphEdge is one aggregated edge in the memory graph visualization.
type GraphEdge struct {
	Source string   `json:"source"`
	Target string   `json:"target"`
	Rel    string   `json:"rel"`
	Rels   []string `json:"rels,omitempty"`
	Count  int      `json:"count"`
	Weight float64  `json:"weight"`
}

// Graph is the public graph projection result for the wiki visualization API.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// BuildGraph projects memory items into concept nodes and aggregated edges for
// the graph visualization. Edge derivation uses PlanFromNodes so the API view
// and recall graph stay aligned. Projection time/label semantics match the
// historical handler path (CreatedAt-only capture with now fallback).
func BuildGraph(botID string, items []Item) Graph {
	nodes, specs, sourceToConcept := buildGraphProjection(botID, items)
	edges := aggregateGraphEdges(projectGraphEdges(PlanFromNodes(specs), sourceToConcept))
	return Graph{Nodes: nodes, Edges: edges}
}

func buildGraphProjection(botID string, items []Item) ([]GraphNode, []NodeSpec, map[string]string) {
	type conceptBucket struct {
		id        string
		slug      string
		count     int
		memoryIDs []string
		item      Item
		spec      NodeSpec
	}

	buckets := map[string]*conceptBucket{}
	order := []string{}
	nodeSpecs := make([]NodeSpec, 0, len(items))
	sourceToConcept := make(map[string]string, len(items))

	for _, item := range items {
		item = canonicalizeItem(botID, item)
		spec := itemToGraphNodeSpec(botID, item)
		if spec.ID == "" {
			continue
		}
		nodeSpecs = append(nodeSpecs, spec)

		slug := NodeSlug(spec.ID, spec.Subject, spec.Topic)
		conceptID := slug
		if conceptID == "" {
			conceptID = spec.ID
		}
		sourceToConcept[spec.ID] = conceptID

		bucket := buckets[conceptID]
		if bucket == nil {
			bucket = &conceptBucket{id: conceptID, slug: slug, item: item, spec: spec}
			buckets[conceptID] = bucket
			order = append(order, conceptID)
		}
		bucket.count++
		bucket.memoryIDs = append(bucket.memoryIDs, item.ID)
		if graphItemRank(item, spec) > graphItemRank(bucket.item, bucket.spec) {
			bucket.item = item
			bucket.spec = spec
		}
	}

	nodes := make([]GraphNode, 0, len(order))
	for _, conceptID := range order {
		bucket := buckets[conceptID]
		sort.Strings(bucket.memoryIDs)
		nodes = append(nodes, GraphNode{
			ID:        bucket.id,
			Label:     graphNodeLabel(bucket.item, bucket.spec),
			Slug:      bucket.slug,
			Memory:    strings.TrimSpace(bucket.item.Memory),
			Subject:   bucket.spec.Subject,
			Topic:     bucket.spec.Topic,
			Count:     bucket.count,
			MemoryIDs: bucket.memoryIDs,
			Metadata:  bucket.item.Metadata,
		})
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Slug != nodes[j].Slug {
			return nodes[i].Slug < nodes[j].Slug
		}
		return nodes[i].ID < nodes[j].ID
	})
	return nodes, nodeSpecs, sourceToConcept
}

func projectGraphEdges(edges []EdgeSpec, sourceToConcept map[string]string) []EdgeSpec {
	if len(edges) == 0 {
		return nil
	}
	projected := make([]EdgeSpec, 0, len(edges))
	for _, edge := range edges {
		source := sourceToConcept[strings.TrimSpace(edge.SrcNode)]
		target := sourceToConcept[strings.TrimSpace(edge.DstNode)]
		if source == "" || target == "" || source == target {
			continue
		}
		edge.SrcNode = source
		edge.DstNode = target
		projected = append(projected, edge)
	}
	return projected
}

func graphNodeLabel(item Item, spec NodeSpec) string {
	label := strings.TrimSpace(spec.Subject)
	if label == "" {
		label = strings.TrimSpace(spec.Topic)
	}
	if label == "" {
		label = strings.TrimSpace(item.Memory)
	}
	if len(label) > 40 {
		label = label[:37] + "..."
	}
	return label
}

func aggregateGraphEdges(edges []EdgeSpec) []GraphEdge {
	type edgeBucket struct {
		source string
		target string
		count  int
		weight float64
		rels   map[string]float64
	}

	buckets := make(map[string]*edgeBucket)
	for _, edge := range edges {
		source := strings.TrimSpace(edge.SrcNode)
		target := strings.TrimSpace(edge.DstNode)
		if source == "" || target == "" || source == target {
			continue
		}
		if target < source {
			source, target = target, source
		}

		key := source + "\x00" + target
		bucket := buckets[key]
		if bucket == nil {
			bucket = &edgeBucket{
				source: source,
				target: target,
				rels:   map[string]float64{},
			}
			buckets[key] = bucket
		}

		rel := strings.TrimSpace(string(edge.Rel))
		if rel == "" {
			rel = "related"
		}
		weight := float64(edge.Weight)
		if weight <= 0 {
			weight = 1
		}
		bucket.count++
		bucket.weight += weight
		bucket.rels[rel] += weight
	}

	out := make([]GraphEdge, 0, len(buckets))
	for _, bucket := range buckets {
		rels := make([]string, 0, len(bucket.rels))
		for rel := range bucket.rels {
			rels = append(rels, rel)
		}
		sort.Slice(rels, func(i, j int) bool {
			left := bucket.rels[rels[i]]
			right := bucket.rels[rels[j]]
			if left != right {
				return left > right
			}
			if graphRelRank(rels[i]) != graphRelRank(rels[j]) {
				return graphRelRank(rels[i]) < graphRelRank(rels[j])
			}
			return rels[i] < rels[j]
		})
		primaryRel := ""
		if len(rels) > 0 {
			primaryRel = rels[0]
		}
		out = append(out, GraphEdge{
			Source: bucket.source,
			Target: bucket.target,
			Rel:    primaryRel,
			Rels:   rels,
			Count:  bucket.count,
			Weight: bucket.weight,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Target < out[j].Target
	})
	return out
}

func graphRelRank(rel string) int {
	switch EdgeRel(rel) {
	case EdgeRefs:
		return 0
	case EdgeSameProfile:
		return 1
	case EdgeSameTopic:
		return 2
	case EdgeSameDay:
		return 3
	default:
		return 100
	}
}

func itemToGraphNodeSpec(botID string, item Item) NodeSpec {
	metadata := item.Metadata
	layer := LayerNote
	if raw, ok := metadata["layer"].(string); ok && strings.TrimSpace(raw) != "" {
		switch Layer(strings.ToLower(strings.TrimSpace(raw))) {
		case LayerPreference, LayerIdentity, LayerContext,
			LayerExperience, LayerActivity, LayerPersona, LayerNote:
			layer = Layer(strings.TrimSpace(raw))
		}
	}
	profileRef := metadataString(metadata, "profile_ref")
	if profileRef == "" {
		profileRef = metadataString(metadata, "profile_user_id")
	}
	return NodeSpec{
		ID:         strings.TrimSpace(item.ID),
		BotID:      botID,
		Body:       strings.TrimSpace(item.Memory),
		Hash:       strings.TrimSpace(item.Hash),
		Layer:      layer,
		Subject:    metadataString(metadata, "subject"),
		Confidence: graphMetadataFloat(metadata, "confidence", 0.5),
		Metadata:   metadata,
		ProfileRef: profileRef,
		Topic:      metadataString(metadata, "topic"),
		CapturedAt: graphParseTime(item.CreatedAt),
	}
}

func graphMetadataFloat(m map[string]any, key string, def float32) float32 {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		f := float32(n)
		if f >= 0 && f <= 1 {
			return f
		}
	case float32:
		if n >= 0 && n <= 1 {
			return n
		}
	}
	return def
}

// graphParseTime preserves the historical visualization path: empty/invalid
// CreatedAt falls back to now (not UpdatedAt).
func graphParseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

func canonicalizeItem(botID string, item Item) Item {
	item.ID = canonicalMemoryID(botID, item.ID)
	if strings.TrimSpace(item.BotID) == "" {
		item.BotID = strings.TrimSpace(botID)
	}
	return item
}

func canonicalMemoryID(botID, id string) string {
	botID = strings.TrimSpace(botID)
	localID := localMemoryID(id)
	if localID == "" {
		return ""
	}
	if botID == "" {
		return localID
	}
	return botID + ":" + localID
}

func localMemoryID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if idx := strings.Index(id, ":"); idx >= 0 && idx+1 < len(id) {
		return strings.TrimSpace(id[idx+1:])
	}
	return id
}

func itemRank(item Item) int {
	score := len(strings.TrimSpace(item.Memory))
	if strings.TrimSpace(item.Hash) != "" {
		score += 50
	}
	if strings.TrimSpace(item.CreatedAt) != "" {
		score += 10
	}
	if strings.TrimSpace(item.UpdatedAt) != "" {
		score += 10
	}
	if item.Metadata != nil {
		score += len(item.Metadata) * 2
		for _, key := range []string{"subject", "topic", "layer", "profile_ref", "confidence"} {
			if metadataString(item.Metadata, key) != "" {
				score += 20
			}
		}
	}
	return score
}

func graphItemRank(item Item, spec NodeSpec) int {
	score := itemRank(item)
	if strings.TrimSpace(spec.Subject) != "" {
		score += 40
	}
	if strings.TrimSpace(spec.Topic) != "" {
		score += 20
	}
	if spec.Layer != "" {
		score += 10
	}
	if strings.TrimSpace(spec.ProfileRef) != "" {
		score += 10
	}
	return score
}
