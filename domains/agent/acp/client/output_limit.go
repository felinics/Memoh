package client

import (
	"strings"
	"unicode/utf8"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/limit"
	"github.com/memohai/memoh/domains/agent/chat/text/prune"
)

type ToolOutputLimit = limit.ToolOutputLimit

func LimitStreamEvent(ev agentdomain.StreamEvent, maxLimit ToolOutputLimit) agentdomain.StreamEvent {
	if !hasToolOutputLimit(maxLimit) || ev.Type != agentdomain.ToolCallEnd {
		return ev
	}
	label := "tool result (" + ev.ToolName + ")"
	ev.Result = limit.LimitToolOutput(ev.Result, label, maxLimit)
	if strings.TrimSpace(ev.Error) != "" {
		ev.Error = limit.LimitString(ev.Error, label, maxLimit)
	}
	return ev
}

func hasToolOutputLimit(limit ToolOutputLimit) bool {
	return limit.MaxBytes > 0 || limit.MaxLines > 0
}

func normalizedToolOutputLimit(maxLimit ToolOutputLimit) ToolOutputLimit {
	return limit.NormalizedLimit(maxLimit)
}

func limitToolOutputString(text, label string, maxLimit ToolOutputLimit) string {
	return limit.LimitString(text, label, maxLimit)
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
