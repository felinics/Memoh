package protocolgen

import (
	"strings"
	"unicode"
)

// initialisms are words rendered in full caps in exported Go identifiers.
var initialisms = map[string]string{
	"id":    "ID",
	"url":   "URL",
	"uri":   "URI",
	"http":  "HTTP",
	"https": "HTTPS",
	"api":   "API",
	"json":  "JSON",
	"uuid":  "UUID",
	"mcp":   "MCP",
	"pid":   "PID",
	"sdp":   "SDP",
}

// exportedName converts a JSON identifier (camelCase, kebab-case, snake_case,
// or slash-separated) into an exported Go identifier.
func exportedName(name string) string {
	var b strings.Builder
	for _, word := range splitWords(name) {
		lower := strings.ToLower(word)
		if repl, ok := initialisms[lower]; ok {
			b.WriteString(repl)
			continue
		}
		r := []rune(word)
		b.WriteString(string(unicode.ToUpper(r[0])) + string(r[1:]))
	}
	return b.String()
}

// isGoIdentifier reports whether s is usable as an exported Go type name.
func isGoIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if unicode.IsLetter(r) || r == '_' || (i > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return unicode.IsUpper([]rune(s)[0])
}

// splitWords breaks an identifier into words on case transitions and on the
// separators `-`, `_`, `/`, `.`, and spaces.
func splitWords(s string) []string {
	var words []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			words = append(words, string(current))
			current = nil
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r == '-' || r == '_' || r == '/' || r == '.' || r == ' ' || r == '$':
			flush()
		case unicode.IsUpper(r):
			// Start a new word on a lower→upper transition, or at the end of
			// an acronym run (upper followed by lower).
			if len(current) > 0 {
				prev := current[len(current)-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					flush()
				}
			}
			current = append(current, r)
		default:
			current = append(current, r)
		}
	}
	flush()
	return words
}
