package timeline

import "github.com/felinics/memoh/internal/agent/turn"

func renderedMessageSource(segment RenderedSegment) *turn.ContextMessageSource {
	kind := "external"
	if segment.IsMyself || segment.IsSelfSent {
		kind = "self"
	}
	return &turn.ContextMessageSource{Kind: kind, ID: segment.MessageID}
}

func markCurrentEntries(entries []mergeEntry, rc RenderedContext, after *DiscussCursorPosition) {
	if after != nil {
		current := make(map[string]bool)
		for _, segment := range rc {
			if !segment.IsMyself && !segment.IsSelfSent && !after.Covers(segment) {
				current[segment.MessageID] = true
			}
		}
		for i := range entries {
			if source := entries[i].source; source != nil && source.Kind == "external" {
				source.Current = current[source.ID]
			}
		}
		return
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if source := entries[i].source; source != nil && source.Kind == "external" {
			source.Current = true
			return
		}
	}
	for _, entry := range entries {
		if entry.source != nil && entry.source.Kind == "self" {
			return
		}
	}
	// Histories without a rendered input retain their existing admission boundary.
	for i := range entries {
		entries[i].source = nil
	}
}
