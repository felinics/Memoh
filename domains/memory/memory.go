// Package memory defines the stable public contract values owned by the Memory
// domain: Item, wiki NodeSpec/EdgeSpec vocabulary, and pure graph planning.
//
// Interfaces (Store/Provider/Registry) stay on the consumer side. This package
// must not import project-internal packages.
package memory

import "time"

// Item is the unique transport-safe memory entry shared by API handlers,
// adapters, and markdown storefs. JSON tags are the wire contract.
type Item struct {
	ID        string         `json:"id"`
	Memory    string         `json:"memory"`
	Hash      string         `json:"hash,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
	Score     float64        `json:"score,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	BotID     string         `json:"bot_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	RunID     string         `json:"run_id,omitempty"`
}

// Layer is the canonical layer a memory node belongs to. Existing flat
// markdown entries that carry no layer hint are classified as LayerNote.
type Layer string

const (
	LayerPreference Layer = "preference"
	LayerIdentity   Layer = "identity"
	LayerContext    Layer = "context"
	LayerExperience Layer = "experience"
	LayerActivity   Layer = "activity"
	LayerPersona    Layer = "persona"
	LayerNote       Layer = "note"
)

// EdgeRel is the canonical relationship type between two memory nodes.
type EdgeRel string

const (
	EdgeSameProfile EdgeRel = "same_profile"
	EdgeSameTopic   EdgeRel = "same_topic"
	EdgeSameDay     EdgeRel = "same_day"
	EdgeRefs        EdgeRel = "refs"
)

// NodeSpec is a backend-agnostic description of a memory_nodes row.
type NodeSpec struct {
	ID               string
	BotID            string
	Body             string
	Hash             string
	Layer            Layer
	FactType         string
	Subject          string
	Confidence       float32
	Metadata         map[string]any
	SourceMessageIDs []string
	ProfileRef       string
	Topic            string
	CapturedAt       time.Time
	ExpiresAt        time.Time // zero value means "no expiry".
}

// EdgeSpec is a backend-agnostic description of a memory_edges row.
type EdgeSpec struct {
	BotID    string
	SrcNode  string
	DstNode  string
	Rel      EdgeRel
	Weight   float32
	Metadata map[string]any
}

// PlanSummary summarises a planned node/edge set for one bot.
type PlanSummary struct {
	BotID      string
	NodeCount  int
	EdgeCount  int
	LayerBreak map[Layer]int
}

// ImplicitEdgeRels is the canonical set of edges derived automatically from
// node attributes (as opposed to explicit refs authored later).
var ImplicitEdgeRels = []EdgeRel{EdgeSameProfile, EdgeSameTopic, EdgeSameDay}

// DerivedEdgeRels is the canonical set of edges rebuilt from node state.
var DerivedEdgeRels = []EdgeRel{EdgeSameProfile, EdgeSameTopic, EdgeSameDay, EdgeRefs}
