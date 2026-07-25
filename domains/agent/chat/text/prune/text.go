package prune

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	DefaultMarker   = "[memoh pruned]"
	DefaultMaxBytes = 64 * 1024
	DefaultMaxLines = 2000
)

type Config struct {
	MaxBytes  int
	MaxLines  int
	HeadBytes int
	TailBytes int
	HeadLines int
	TailLines int
	Marker    string
}

func Exceeds(s string, maxBytes, maxLines int) bool {
	return len(s) > maxBytes || CountLines(s) > maxLines
}

func CountLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func PruneWithEdges(s, label string, cfg Config) string {
	cfg = normalizeConfig(cfg)
	if len(s) == 0 {
		return s
	}
	if cfg.HeadBytes+cfg.TailBytes <= 0 || cfg.HeadLines+cfg.TailLines <= 0 {
		return fitBudget(fmt.Sprintf(
			"%s %s omitted (bytes=%d, lines=%d)",
			cfg.Marker,
			label,
			len(s),
			CountLines(s),
		), cfg)
	}
	if !Exceeds(s, cfg.MaxBytes, cfg.MaxLines) {
		return s
	}
	head := boundedPrefix(s, minInt(cfg.HeadBytes, len(s)), cfg.HeadLines)
	tail := ""
	if cfg.TailBytes > 0 && cfg.TailLines > 0 {
		tail = boundedSuffix(s, minInt(cfg.TailBytes, len(s)), cfg.TailLines)
	}
	header := fmt.Sprintf(
		"%s %s too long (bytes=%d, lines=%d), showing head/tail\n\n",
		cfg.Marker,
		label,
		len(s),
		CountLines(s),
	)
	return fitHeadTailBudget(header, head, tail, cfg)
}

func normalizeConfig(cfg Config) Config {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.MaxLines <= 0 {
		cfg.MaxLines = DefaultMaxLines
	}
	if cfg.Marker == "" {
		cfg.Marker = DefaultMarker
	}
	if cfg.HeadBytes < 0 {
		cfg.HeadBytes = 0
	}
	if cfg.TailBytes < 0 {
		cfg.TailBytes = 0
	}
	if cfg.HeadLines < 0 {
		cfg.HeadLines = 0
	}
	if cfg.TailLines < 0 {
		cfg.TailLines = 0
	}
	return cfg
}

func fitBudget(s string, cfg Config) string {
	if !Exceeds(s, cfg.MaxBytes, cfg.MaxLines) {
		return s
	}
	trimmed := boundedPrefix(s, cfg.MaxBytes, cfg.MaxLines)
	if trimmed == "" {
		return cfg.Marker
	}
	return trimmed
}

func fitHeadTailBudget(header, head, tail string, cfg Config) string {
	if tail == "" {
		return fitBudget(header+head, cfg)
	}
	headTailSeparator := "\n\n[...snip...]\n\n"
	for {
		candidate := header + head + headTailSeparator + tail
		if !Exceeds(candidate, cfg.MaxBytes, cfg.MaxLines) {
			return candidate
		}
		nextHead, nextTail := shrinkHeadTail(head, tail)
		if nextHead == head && nextTail == tail {
			break
		}
		head, tail = nextHead, nextTail
	}
	return fitBudgetPreserveSuffix(header+head+headTailSeparator, tail, cfg)
}

func shrinkHeadTail(head, tail string) (string, string) {
	if len(head) == 0 && len(tail) == 0 {
		return head, tail
	}
	if len(head) >= len(tail) && (len(head) > 1 || CountLines(head) > 1) {
		return shrinkPrefix(head), tail
	}
	if len(tail) > 1 || CountLines(tail) > 1 {
		return head, shrinkSuffix(tail)
	}
	if len(head) > 0 {
		return "", tail
	}
	return head, ""
}

func shrinkPrefix(s string) string {
	return boundedPrefix(s, reducePositive(len(s)), reduceLines(CountLines(s)))
}

func shrinkSuffix(s string) string {
	return boundedSuffix(s, reducePositive(len(s)), reduceLines(CountLines(s)))
}

func reducePositive(n int) int {
	if n <= 1 {
		return 0
	}
	next := n * 3 / 4
	if next >= n {
		next = n - 1
	}
	return next
}

func reduceLines(n int) int {
	if n <= 1 {
		return 1
	}
	return reducePositive(n)
}

func fitBudgetPreserveSuffix(prefix, suffix string, cfg Config) string {
	if !Exceeds(prefix+suffix, cfg.MaxBytes, cfg.MaxLines) {
		return prefix + suffix
	}
	prefixBudget := cfg.MaxBytes / 2
	prefixLines := cfg.MaxLines / 2
	if prefixLines <= 0 {
		prefixLines = 1
	}
	trimmedPrefix := boundedPrefix(prefix, prefixBudget, prefixLines)
	remainingBytes := cfg.MaxBytes - len(trimmedPrefix)
	if remainingBytes <= 0 {
		return fitBudget(prefix, cfg)
	}
	remainingLines := cfg.MaxLines - CountLines(trimmedPrefix)
	if remainingLines <= 0 {
		remainingLines = 1
	}
	trimmedSuffix := boundedSuffix(suffix, remainingBytes, remainingLines)
	if trimmedSuffix == "" {
		return fitBudget(prefix, cfg)
	}
	candidate := trimmedPrefix + trimmedSuffix
	if !Exceeds(candidate, cfg.MaxBytes, cfg.MaxLines) {
		return candidate
	}
	return fitBudget(candidate, cfg)
}

func boundedPrefix(s string, maxBytes, maxLines int) string {
	if len(s) == 0 || maxBytes <= 0 || maxLines <= 0 {
		return ""
	}
	prefix := safeUTF8Prefix(s, minInt(maxBytes, len(s)))
	return limitLinesPrefix(prefix, maxLines)
}

func boundedSuffix(s string, maxBytes, maxLines int) string {
	if len(s) == 0 || maxBytes <= 0 || maxLines <= 0 {
		return ""
	}
	suffix := safeUTF8Suffix(s, minInt(maxBytes, len(s)))
	return limitLinesSuffix(suffix, maxLines)
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

func safeUTF8Suffix(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) == 0 {
		return ""
	}
	if maxBytes >= len(s) {
		return s
	}
	start := len(s) - maxBytes
	if start < 0 {
		start = 0
	}
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	if start >= len(s) {
		return ""
	}
	return s[start:]
}

func limitLinesPrefix(s string, maxLines int) string {
	if maxLines <= 0 || s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n")
}

func limitLinesSuffix(s string, maxLines int) string {
	if maxLines <= 0 || s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[len(lines)-maxLines:], "\n")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
