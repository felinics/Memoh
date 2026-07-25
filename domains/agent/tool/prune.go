package tool

import (
	textprune "github.com/memohai/memoh/domains/agent/chat/text/prune"
)

const (
	toolOutputHeadBytes = 32 * 1024
	toolOutputTailBytes = 8 * 1024
	toolOutputHeadLines = 500
	toolOutputTailLines = 100

	listMaxEntries        = 200
	listCollapseThreshold = 50
)

func pruneToolOutputText(text, label string) string {
	return textprune.PruneWithEdges(text, label, textprune.Config{
		MaxBytes:  textprune.DefaultMaxBytes,
		MaxLines:  textprune.DefaultMaxLines,
		HeadBytes: toolOutputHeadBytes,
		TailBytes: toolOutputTailBytes,
		HeadLines: toolOutputHeadLines,
		TailLines: toolOutputTailLines,
		Marker:    textprune.DefaultMarker,
	})
}
