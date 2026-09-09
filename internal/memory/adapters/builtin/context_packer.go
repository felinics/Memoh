package builtin

import (
	"sort"
	"strings"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
)

// contextPackerConfig controls how memory items are packed into a context
// window with a fixed character budget.
type contextPackerConfig struct {
	TargetItems    int // desired number of items in final context
	MaxTotalChars  int // hard budget for combined snippet length
	MinItemChars   int // minimum snippet length per item
	MaxItemChars   int // maximum snippet length per item
	OverfetchRatio int // fetch TargetItems * OverfetchRatio candidates
}

var defaultPackerConfig = contextPackerConfig{
	TargetItems:    6,
	MaxTotalChars:  1800,
	MinItemChars:   80,
	MaxItemChars:   360,
	OverfetchRatio: 3,
}

// contextPackResult contains the items selected for context injection.
type contextPackResult struct {
	Items []packedItem
}

type packedItem struct {
	Item    adapters.MemoryItem
	Snippet string
}

// packContext selects and truncates memory items to fit within the character
// budget. Items must already be deduplicated and sorted by score descending.
//
// The algorithm:
//  1. Assign each item its full text or MaxItemChars, whichever is shorter.
//  2. Walk items in score order; greedily include items while budget allows.
//  3. If we haven't reached TargetItems, try compressing already-included
//     items to make room for more.
//  4. Apply anti-lost-in-the-middle reordering: best items at head and tail.
func packContext(items []adapters.MemoryItem, cfg contextPackerConfig) contextPackResult {
	if len(items) == 0 {
		return contextPackResult{}
	}
	if cfg.TargetItems <= 0 {
		cfg.TargetItems = defaultPackerConfig.TargetItems
	}
	if cfg.MaxTotalChars <= 0 {
		cfg.MaxTotalChars = defaultPackerConfig.MaxTotalChars
	}
	if cfg.MinItemChars <= 0 {
		cfg.MinItemChars = defaultPackerConfig.MinItemChars
	}
	if cfg.MaxItemChars <= 0 {
		cfg.MaxItemChars = defaultPackerConfig.MaxItemChars
	}

	type candidate struct {
		item    adapters.MemoryItem
		text    string
		charLen int
	}
	candidates := make([]candidate, 0, len(items))
	for _, it := range items {
		text := strings.TrimSpace(it.Memory)
		if text == "" {
			continue
		}
		runes := []rune(text)
		cl := len(runes)
		if cl > cfg.MaxItemChars {
			cl = cfg.MaxItemChars
		}
		candidates = append(candidates, candidate{item: it, text: text, charLen: cl})
	}

	// Phase 1: greedily pack items at their natural (capped) length.
	selected := make([]candidate, 0, cfg.TargetItems)
	usedChars := 0
	for _, c := range candidates {
		if len(selected) >= cfg.TargetItems {
			break
		}
		if usedChars+c.charLen > cfg.MaxTotalChars {
			// Try with minimum length.
			if usedChars+cfg.MinItemChars > cfg.MaxTotalChars {
				continue
			}
			c.charLen = cfg.MinItemChars
		}
		selected = append(selected, c)
		usedChars += c.charLen
	}

	// Phase 2: if we didn't reach TargetItems, try compressing existing items
	// to free budget for more candidates.
	if len(selected) < cfg.TargetItems && len(selected) < len(candidates) {
		for i := range selected {
			if selected[i].charLen > cfg.MinItemChars {
				freed := selected[i].charLen - cfg.MinItemChars
				selected[i].charLen = cfg.MinItemChars
				usedChars -= freed
			}
		}
		for _, c := range candidates[len(selected):] {
			if len(selected) >= cfg.TargetItems {
				break
			}
			needed := c.charLen
			if needed > cfg.MaxTotalChars-usedChars {
				needed = cfg.MaxTotalChars - usedChars
			}
			if needed < cfg.MinItemChars {
				continue
			}
			c.charLen = needed
			selected = append(selected, c)
			usedChars += needed
		}
	}

	// Phase 3: redistribute remaining budget to compressed items.
	remaining := cfg.MaxTotalChars - usedChars
	if remaining > 0 && len(selected) > 0 {
		perItem := remaining / len(selected)
		for i := range selected {
			textLen := len([]rune(selected[i].text))
			maxGrow := cfg.MaxItemChars - selected[i].charLen
			if maxGrow > textLen-selected[i].charLen {
				maxGrow = textLen - selected[i].charLen
			}
			if maxGrow <= 0 {
				continue
			}
			grow := perItem
			if grow > maxGrow {
				grow = maxGrow
			}
			selected[i].charLen += grow
			usedChars += grow
		}
	}

	// Phase 4: anti-lost-in-the-middle reordering.
	// Place best items at positions 0 and last; fill middle with the rest.
	reordered := antiLostInMiddle(selected)

	result := make([]packedItem, 0, len(reordered))
	for _, c := range reordered {
		snippet := adapters.TruncateSnippet(c.text, c.charLen)
		result = append(result, packedItem{Item: c.item, Snippet: snippet})
	}
	return contextPackResult{Items: result}
}

// antiLostInMiddle reorders candidates so the highest-scored items appear at
// the beginning and end of the sequence, reducing the "lost in the middle"
// effect observed in LLMs.
func antiLostInMiddle[T any](items []T) []T {
	n := len(items)
	if n <= 2 {
		return items
	}
	out := make([]T, n)
	head, tail := 0, n-1
	for i, item := range items {
		if i%2 == 0 {
			out[head] = item
			head++
		} else {
			out[tail] = item
			tail--
		}
	}
	return out
}

// overfetchLimit returns the number of candidates to request from the backend,
// given the desired target item count and overfetch ratio.
func overfetchLimit(cfg contextPackerConfig) int {
	limit := cfg.TargetItems * cfg.OverfetchRatio
	if limit < cfg.TargetItems {
		limit = cfg.TargetItems
	}
	return limit
}

// deduplicateAndSort removes duplicate items by ID, then sorts by score desc.
func deduplicateAndSort(items []adapters.MemoryItem) []adapters.MemoryItem {
	deduped := adapters.DeduplicateItems(items)
	sort.Slice(deduped, func(i, j int) bool {
		return deduped[i].Score > deduped[j].Score
	})
	return deduped
}
