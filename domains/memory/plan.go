package memory

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// PlanFromItems converts markdown-backed memory items into wiki node specs plus
// derived edges from shared profile_ref/topic/captured day and explicit wiki
// cross-references in node bodies. It performs no I/O.
//
// Layer classification is intentionally conservative: items without any hint
// fall back to LayerNote.
func PlanFromItems(botID string, items []Item) ([]NodeSpec, []EdgeSpec) {
	nodes := make([]NodeSpec, 0, len(items))
	for _, item := range items {
		nodes = append(nodes, nodeFromItem(botID, item))
	}
	return nodes, PlanFromNodes(nodes)
}

// PlanFromNodes derives graph edges from an existing set of nodes without
// re-classifying them. It is the edge-derivation half of PlanFromItems.
func PlanFromNodes(nodes []NodeSpec) []EdgeSpec {
	return buildDerivedEdges(nodes)
}

// Summarise returns a PlanSummary for a planned node/edge set.
func Summarise(botID string, nodes []NodeSpec, edges []EdgeSpec) PlanSummary {
	r := PlanSummary{BotID: botID, NodeCount: len(nodes), EdgeCount: len(edges), LayerBreak: map[Layer]int{}}
	for _, n := range nodes {
		layer := n.Layer
		if layer == "" {
			layer = LayerNote
		}
		r.LayerBreak[layer]++
	}
	return r
}

func nodeFromItem(botID string, item Item) NodeSpec {
	body := strings.TrimSpace(item.Memory)
	layer := classifyLayer(item)
	topic := metadataString(item.Metadata, "topic")
	profileRef := metadataString(item.Metadata, "profile_ref")
	if profileRef == "" {
		profileRef = metadataString(item.Metadata, "profile_user_id")
	}
	captured := parseTime(item.CreatedAt)
	if captured.IsZero() {
		captured = parseTime(item.UpdatedAt)
	}
	if captured.IsZero() {
		captured = time.Now().UTC()
	}
	return NodeSpec{
		ID:         strings.TrimSpace(item.ID),
		BotID:      botID,
		Body:       body,
		Hash:       strings.TrimSpace(item.Hash),
		Layer:      layer,
		Subject:    metadataString(item.Metadata, "subject"),
		Confidence: metadataFloat(item.Metadata, "confidence", 0.5),
		Metadata:   cloneMetadata(item.Metadata),
		ProfileRef: profileRef,
		Topic:      topic,
		CapturedAt: captured,
	}
}

func classifyLayer(item Item) Layer {
	if raw := metadataString(item.Metadata, "layer"); raw != "" {
		switch Layer(strings.ToLower(strings.TrimSpace(raw))) {
		case LayerPreference, LayerIdentity, LayerContext, LayerExperience, LayerActivity, LayerPersona, LayerNote:
			return Layer(raw)
		}
	}
	return LayerNote
}

func buildImplicitEdges(nodes []NodeSpec) []EdgeSpec {
	if len(nodes) < 2 {
		return nil
	}
	byProfile := indexBy(nodes, func(n NodeSpec) string { return n.ProfileRef })
	byTopic := indexBy(nodes, func(n NodeSpec) string { return n.Topic })
	byDay := indexBy(nodes, func(n NodeSpec) string { return n.CapturedAt.UTC().Format("2006-01-02") })

	seen := map[string]struct{}{}
	edges := make([]EdgeSpec, 0)
	add := func(a, b NodeSpec, rel EdgeRel, weight float32) {
		if a.ID == b.ID {
			return
		}
		src, dst := a.ID, b.ID
		if dst < src {
			src, dst = dst, src
		}
		key := src + "\x00" + dst + "\x00" + string(rel)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		edges = append(edges, EdgeSpec{BotID: a.BotID, SrcNode: src, DstNode: dst, Rel: rel, Weight: weight})
	}

	emit := func(groups [][]NodeSpec, rel EdgeRel, weight float32) {
		for _, group := range groups {
			if len(group) < 2 {
				continue
			}
			for i := 0; i < len(group); i++ {
				for j := i + 1; j < len(group); j++ {
					add(group[i], group[j], rel, weight)
				}
			}
		}
	}
	emit(byProfile, EdgeSameProfile, 1.0)
	emit(byTopic, EdgeSameTopic, 0.8)
	emit(byDay, EdgeSameDay, 0.5)

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Rel != edges[j].Rel {
			return edges[i].Rel < edges[j].Rel
		}
		if edges[i].SrcNode != edges[j].SrcNode {
			return edges[i].SrcNode < edges[j].SrcNode
		}
		return edges[i].DstNode < edges[j].DstNode
	})
	return edges
}

func buildDerivedEdges(nodes []NodeSpec) []EdgeSpec {
	edges := buildImplicitEdges(nodes)
	edges = append(edges, buildRefEdges(nodes)...)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Rel != edges[j].Rel {
			return edges[i].Rel < edges[j].Rel
		}
		if edges[i].SrcNode != edges[j].SrcNode {
			return edges[i].SrcNode < edges[j].SrcNode
		}
		return edges[i].DstNode < edges[j].DstNode
	})
	return edges
}

func buildRefEdges(nodes []NodeSpec) []EdgeSpec {
	if len(nodes) < 2 {
		return nil
	}
	slugToID := make(map[string]string, len(nodes))
	for _, node := range nodes {
		if slug := NodeSlug(node.ID, node.Subject, node.Topic); slug != "" {
			slugToID[slug] = node.ID
		}
	}
	seen := map[string]struct{}{}
	edges := make([]EdgeSpec, 0)
	for _, src := range nodes {
		for _, raw := range parseMemoryLinks(src.Body) {
			dstID, ok := slugToID[slugify(raw)]
			if !ok || dstID == src.ID {
				continue
			}
			key := src.ID + "\x00" + dstID + "\x00" + string(EdgeRefs)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			edges = append(edges, EdgeSpec{
				BotID:   src.BotID,
				SrcNode: src.ID,
				DstNode: dstID,
				Rel:     EdgeRefs,
				Weight:  1.0,
			})
		}
	}
	return edges
}

func indexBy(nodes []NodeSpec, key func(NodeSpec) string) [][]NodeSpec {
	buckets := map[string][]NodeSpec{}
	order := []string{}
	for _, n := range nodes {
		k := strings.TrimSpace(key(n))
		if k == "" {
			continue
		}
		if _, ok := buckets[k]; !ok {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], n)
	}
	out := make([][]NodeSpec, 0, len(buckets))
	for _, k := range order {
		out = append(out, buckets[k])
	}
	return out
}

func metadataString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s)
	default:
		return strings.TrimSpace(toString(v))
	}
}

func metadataFloat(m map[string]any, key string, def float32) float32 {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return clamp32(float32(n), def)
	case float32:
		return clamp32(n, def)
	case int:
		return clamp32(float32(n), def)
	case int64:
		return clamp32(float32(n), def)
	case string:
		return def
	default:
		return def
	}
}

func clamp32(v, def float32) float32 {
	if v < 0 || v > 1 {
		return def
	}
	return v
}

func cloneMetadata(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
