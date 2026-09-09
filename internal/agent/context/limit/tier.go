package contextlimit

import (
	textprune "github.com/felinics/memoh/internal/prune"
)

// Tier is a named layered truncation policy: MaxBytes/MaxLines are the outer
// ceiling that decides whether truncation engages at all; HeadBytes/TailBytes
// and HeadLines/TailLines are the retained head/tail split once it does.
type Tier struct {
	MaxBytes  int
	MaxLines  int
	HeadBytes int
	TailBytes int
	HeadLines int
	TailLines int
	Marker    string
}

// LimitString truncates s to this tier's head/tail budget, UTF-8 safely,
// once s exceeds the tier's ceiling.
func (t Tier) LimitString(s, label string) string {
	return textprune.PruneWithEdges(s, label, textprune.Config{
		MaxBytes:  t.MaxBytes,
		MaxLines:  t.MaxLines,
		HeadBytes: t.HeadBytes,
		TailBytes: t.TailBytes,
		HeadLines: t.HeadLines,
		TailLines: t.TailLines,
		Marker:    t.Marker,
	})
}

var (
	// PruneTier matches internal/agent/tools' pre-unification tool-output
	// pruning (exec stdout/stderr, etc.): the same head/tail split contextlimit
	// itself already defaults to.
	PruneTier = Tier{
		MaxBytes:  textprune.DefaultMaxBytes,
		MaxLines:  textprune.DefaultMaxLines,
		HeadBytes: toolOutputHeadBytes,
		TailBytes: toolOutputTailBytes,
		HeadLines: toolOutputHeadLines,
		TailLines: toolOutputTailLines,
		Marker:    textprune.DefaultMarker,
	}

	// GatewayResultTier matches internal/conversation/flow's gateway prune for
	// tool-result content.
	GatewayResultTier = Tier{
		MaxBytes:  textprune.DefaultMaxBytes,
		MaxLines:  textprune.DefaultMaxLines,
		HeadBytes: 6 * 1024,
		TailBytes: 2 * 1024,
		HeadLines: 180,
		TailLines: 50,
		Marker:    textprune.DefaultMarker,
	}

	// GatewayArgsTier matches internal/conversation/flow's gateway prune for
	// tool-call arguments.
	GatewayArgsTier = Tier{
		MaxBytes:  textprune.DefaultMaxBytes,
		MaxLines:  textprune.DefaultMaxLines,
		HeadBytes: 4 * 1024,
		TailBytes: 2 * 1024,
		HeadLines: 180,
		TailLines: 50,
		Marker:    textprune.DefaultMarker,
	}
)
