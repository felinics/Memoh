package wikistore

import (
	"encoding/json"
	"strings"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/memory/migrate"
)

// ---- NodeSpec <-> wire record helpers ----

// nodeToRecord flattens a NodeSpec into the internal record shape. The
// PostgreSQL implementation calls this before building its driver-specific
// sqlc params, so the metadata JSON marshalling stays in one place.
func nodeToRecord(n migrate.NodeSpec) record {
	return record{
		ID:               n.ID,
		BotID:            n.BotID,
		Body:             n.Body,
		Hash:             n.Hash,
		Layer:            string(orDefaultLayer(n.Layer)),
		FactType:         n.FactType,
		Subject:          n.Subject,
		Confidence:       clampConfidence(n.Confidence),
		Metadata:         n.Metadata,
		SourceMessageIDs: adapters.NormalizeSourceRefs(n.SourceMessageIDs),
		ProfileRef:       n.ProfileRef,
		Topic:            n.Topic,
		CapturedAt:       n.CapturedAt,
		ExpiresAt:        n.ExpiresAt,
	}
}

func recordToNode(r record) migrate.NodeSpec {
	return migrate.NodeSpec{
		ID:               r.ID,
		BotID:            r.BotID,
		Body:             r.Body,
		Hash:             r.Hash,
		Layer:            migrate.MemoryLayer(r.Layer),
		FactType:         r.FactType,
		Subject:          r.Subject,
		Confidence:       r.Confidence,
		Metadata:         r.Metadata,
		SourceMessageIDs: adapters.NormalizeSourceRefs(r.SourceMessageIDs),
		ProfileRef:       r.ProfileRef,
		Topic:            r.Topic,
		CapturedAt:       r.CapturedAt,
		ExpiresAt:        r.ExpiresAt,
	}
}

func orDefaultLayer(layer migrate.MemoryLayer) migrate.MemoryLayer {
	if layer = migrate.MemoryLayer(strings.TrimSpace(string(layer))); layer == "" {
		return migrate.LayerNote
	}
	return layer
}

func clampConfidence(c float32) float32 {
	if c < 0 || c > 1 {
		return 0.5
	}
	return c
}

// marshalJSON serialises a metadata map to a JSON byte slice (empty -> "{}").
func marshalJSON(m map[string]any) []byte {
	if len(m) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// unmarshalMetadata parses a JSON byte slice into a map[string]any.
func unmarshalMetadata(b []byte) map[string]any {
	if len(b) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// marshalStringList serialises a string slice to a JSON array byte slice.
func marshalStringList(s []string) []byte {
	if len(s) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return []byte("[]")
	}
	return b
}

func unmarshalStringList(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	var s []string
	if err := json.Unmarshal(b, &s); err != nil {
		return nil
	}
	return s
}

// filterImplicitEdges keeps only edges whose Rel is in the allowed set. Used
// when rebuilding derived edges so only same_profile / same_topic / same_day /
// refs edges are written back.
func filterImplicitEdges(edges []migrate.EdgeSpec, allowed []migrate.EdgeRel) []migrate.EdgeSpec {
	if len(edges) == 0 {
		return nil
	}
	set := make(map[migrate.EdgeRel]struct{}, len(allowed))
	for _, r := range allowed {
		set[r] = struct{}{}
	}
	out := make([]migrate.EdgeSpec, 0, len(edges))
	for _, e := range edges {
		if _, ok := set[e.Rel]; ok {
			out = append(out, e)
		}
	}
	return out
}
