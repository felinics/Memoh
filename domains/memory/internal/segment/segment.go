// Package segment tokenises memory text for lexical scoring, with first-class
// support for CJK (Chinese/Japanese) text.
//
// The graph and file memory runtimes score candidate memories against the chat
// query by counting token overlaps. The original implementation used
// strings.Fields, which splits only on Unicode whitespace — CJK text has no
// inter-word spaces, so a whole Chinese sentence collapsed into a single giant
// token that never matched a memory body (lexical score 0 for any natural
// Chinese query). This package routes CJK runs through gse (a pure-Go jieba
// port with an embedded dictionary) and leaves Latin/numeric runs on
// strings.Fields, so English/mixed behaviour is unchanged.
package segment

import (
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
)

var (
	seg    gse.Segmenter
	segErr error
	once   sync.Once
)

// loadInitialises the embedded zh dictionary exactly once. After it returns the
// segmenter is read-only and safe for concurrent use. A load failure is recorded
// in segErr; Tokens then degrades to strings.Fields so memory search keeps
// working (merely without CJK word boundaries) rather than panicking.
func load() {
	seg.SkipLog = true
	seg, segErr = gse.NewEmbed("zh") // simplified + traditional, no runtime files
}

// ready reports whether the gse segmenter is available.
func ready() bool {
	once.Do(load)
	return segErr == nil
}

// isCJK reports whether r belongs to a CJK (or fullwidth) Unicode block that gse
// should segment. Whitespace, Latin, digits, and ASCII punctuation return false
// so they stay on the strings.Fields path.
func isCJK(r rune) bool {
	switch {
	case unicode.In(r, unicode.Han), // CJK Unified Ideographs (zh/ja common)
		unicode.In(r, unicode.Hiragana),
		unicode.In(r, unicode.Katakana),
		unicode.In(r, unicode.Hangul):
		return true
	}
	return false
}

// hasCJK reports whether s contains any CJK rune.
func hasCJK(s string) bool {
	for _, r := range s {
		if isCJK(r) {
			return true
		}
	}
	return false
}

// isTokenRune reports whether r is a rune worth keeping as/inside a token:
// letters, digits, and CJK ideographs. Punctuation, symbols, and whitespace are
// dropped (gse already emits punctuation as standalone tokens; we filter them).
func isTokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// Tokens splits text into lowercase word tokens for lexical matching.
//
// Latin/numeric runs are split on whitespace (matching the historical
// strings.Fields behaviour). CJK runs are segmented by gse in search-engine
// mode (HMM on) which emits both whole words and their sub-words for better
// recall. Punctuation, whitespace, and empty fragments are dropped.
//
// When the gse dictionary failed to load, Tokens degrades to strings.Fields so
// search keeps working without CJK word boundaries.
func Tokens(text string) []string {
	text = strings.ToLower(text)
	if !hasCJK(text) || !ready() {
		return fieldsFiltered(text)
	}

	tokens := make([]string, 0, 8)
	for _, run := range splitCJKRuns(text) {
		run = strings.TrimSpace(run)
		if run == "" {
			continue
		}
		if !hasCJK(run) {
			// Pure Latin/digit run: keep the whitespace-split behaviour.
			tokens = append(tokens, fieldsFiltered(run)...)
			continue
		}
		for _, w := range seg.CutSearch(run, true) {
			if w = strings.TrimSpace(w); w != "" && isTokenRune([]rune(w)[0]) {
				tokens = append(tokens, w)
			}
		}
	}
	return tokens
}

// splitCJKRuns splits text into alternating runs, each either wholly CJK or
// wholly non-CJK, preserving order. This lets us apply gse only to CJK spans.
func splitCJKRuns(s string) []string {
	var runs []string
	var cur strings.Builder
	curInCJK := false
	flush := func() {
		if cur.Len() > 0 {
			runs = append(runs, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		inCJK := isCJK(r)
		if cur.Len() > 0 && inCJK != curInCJK {
			flush()
		}
		curInCJK = inCJK
		cur.WriteRune(r)
	}
	flush()
	return runs
}

// fieldsFiltered splits s on whitespace and drops any non-token fragments.
func fieldsFiltered(s string) []string {
	out := make([]string, 0, 4)
	for _, f := range strings.Fields(s) {
		if isTokenRune([]rune(f)[0]) {
			out = append(out, f)
		}
	}
	return out
}

// LexicalScore scores how well body covers the tokens of query, in [0, 1].
//
// It mirrors the historical graphLexicalScore / fileRuntimeScore contract:
//   - empty query returns 1 (caller treats "no query" as a neutral match);
//   - if query is a substring of body, returns 1 (fast path, exact phrase);
//   - otherwise, the fraction of query tokens that appear as substrings of body.
//
// The only behavioural change from the legacy implementation is that Tokens
// (rather than strings.Fields) defines what a "token" is, so Chinese sentences
// are split into words and can now match memory bodies.
func LexicalScore(query, body string) float64 {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 1
	}
	body = strings.ToLower(body)
	if strings.Contains(body, query) {
		return 1
	}
	tokens := Tokens(query)
	if len(tokens) == 0 {
		return 0
	}
	hits := 0
	for _, token := range tokens {
		if strings.Contains(body, token) {
			hits++
		}
	}
	return float64(hits) / float64(len(tokens))
}
