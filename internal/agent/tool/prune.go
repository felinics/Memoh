package tools

import (
	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
)

const (
	listMaxEntries        = 200
	listCollapseThreshold = 50
)

func pruneToolOutputText(text, label string) string {
	return contextlimit.PruneTier.LimitString(text, label)
}
