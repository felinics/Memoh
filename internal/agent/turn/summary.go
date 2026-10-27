package turn

// PlaceSummary renders summary in place of the items it covers. Every other
// item keeps its order. The summary goes right after the last kept item
// inside the covered span, or where the first covered item was when no kept
// item lies inside it: items the span holds but the summary does not cover
// render before it. Without a covered item, items come back unchanged and
// summary is not placed.
//
// The in-run splice, the record-path history loader and the pipeline
// composer all place a summary by this rule, so a summary renders at the
// same relative place in the request that wrote it and in every later one.
// A later request that does not load the last item a summary covers places
// it where that item was, by the summary's durable end anchor.
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
