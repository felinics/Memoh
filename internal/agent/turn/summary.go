package turn

import "strings"

// summaryPreamble says what a rendered summary stands for and what wins over
// it. The text is model-visible and part of every request prefix that holds a
// summary; change it only deliberately.
const summaryPreamble = "The summary below stands for earlier messages of this conversation that are no longer shown; the messages shown verbatim before it and everything after it take precedence over it."

// SummaryText is the text a summary renders as wherever a request carries
// it: the preamble, then the summary in its tags.
func SummaryText(summary string) string {
	return summaryPreamble + "\n<summary>\n" + strings.TrimSpace(summary) + "\n</summary>"
}

// PlaceSummary renders summary in place of the items it covers. Every other
// item keeps its order. The summary goes right after the last kept item
// inside the covered span, or where the first covered item was when no kept
// item lies inside it: items the span holds but the summary does not cover
// render before it. Without a covered item, items come back unchanged and
// summary is not placed.
//
// A fold orders the request it leaves by this rule, which the in-run splice
// and a request built from rows apply, and a history checkpoint orders the
// rows a request read the same way: the ones it keeps, then its summary. A summary then renders at the same relative place in the request
// that wrote it and in every later one.
func PlaceSummary[T any](items []T, summary T, covered func(T) bool) ([]T, bool) {
	last := -1
	for i, item := range items {
		if covered(item) {
			last = i
		}
	}
	if last < 0 {
		return items, false
	}
	out := make([]T, 0, len(items)+1)
	for i, item := range items {
		if !covered(item) {
			out = append(out, item)
		}
		if i == last {
			out = append(out, summary)
		}
	}
	return out, true
}
