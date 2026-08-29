package client

import (
	"strings"
	"unicode/utf8"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/prune"
)

type ToolOutputLimit = contextlimit.ToolOutputLimit

func LimitStreamEvent(ev event.StreamEvent, limit ToolOutputLimit) event.StreamEvent {
	if !hasToolOutputLimit(limit) || ev.Type != event.ToolCallEnd {
		return ev
	}
	label := "tool result (" + ev.ToolName + ")"
	ev.Result = contextlimit.LimitToolOutput(ev.Result, label, limit)
	if strings.TrimSpace(ev.Error) != "" {
		ev.Error = contextlimit.LimitString(ev.Error, label, limit)
	}
	return ev
}

func hasToolOutputLimit(limit ToolOutputLimit) bool {
	return limit.MaxBytes > 0 || limit.MaxLines > 0
}

func normalizedToolOutputLimit(limit ToolOutputLimit) ToolOutputLimit {
	return contextlimit.NormalizedLimit(limit)
}

func limitToolOutputString(text, label string, limit ToolOutputLimit) string {
	return contextlimit.LimitString(text, label, limit)
}

func limitToolOutputStringExact(text, label string, limit ToolOutputLimit) string {
	if !hasToolOutputLimit(limit) {
		return text
	}
	maxBytes := limit.MaxBytes
	if maxBytes <= 0 {
		maxBytes = prune.DefaultMaxBytes
	}
	maxLines := limit.MaxLines
	if maxLines <= 0 {
		maxLines = prune.DefaultMaxLines
	}
	headBytes := maxBytes * 3 / 4
	tailBytes := maxBytes - headBytes
	headLines := maxLines * 3 / 4
	tailLines := maxLines - headLines
	limited := prune.PruneWithEdges(text, label, prune.Config{
		MaxBytes:  maxBytes,
		MaxLines:  maxLines,
		HeadBytes: headBytes,
		TailBytes: tailBytes,
		HeadLines: headLines,
		TailLines: tailLines,
	})
	if limit.MaxBytes > 0 && len(limited) > limit.MaxBytes {
		return safeUTF8Prefix(limited, limit.MaxBytes)
	}
	return limited
}

func safeUTF8Prefix(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) == 0 {
		return ""
	}
	if maxBytes >= len(s) {
		return s
	}
	cut := maxBytes
	for cut > 0 && cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut <= 0 {
		return ""
	}
	return s[:cut]
}
