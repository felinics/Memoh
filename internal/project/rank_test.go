package project

import (
	"sort"
	"strings"
	"testing"
)

func TestRankInitial(t *testing.T) {
	r := rankInitial()
	if r == "" {
		t.Fatal("initial rank is empty")
	}
	if strings.HasSuffix(r, string(rankDigits[0])) {
		t.Fatalf("initial rank %q ends with the zero digit", r)
	}
}

func TestRankAfterChainStaysOrdered(t *testing.T) {
	last := ""
	for range 200 {
		next, err := rankAfter(last)
		if err != nil {
			t.Fatalf("rankAfter(%q): %v", last, err)
		}
		if last != "" && next <= last {
			t.Fatalf("rankAfter(%q) = %q, not greater", last, next)
		}
		if strings.HasSuffix(next, string(rankDigits[0])) {
			t.Fatalf("rank %q ends with the zero digit", next)
		}
		last = next
	}
	if len(last) > 10 {
		t.Fatalf("appending 200 items grew the rank to %d chars: %q", len(last), last)
	}
}

func TestRankBetweenStrictlyBetween(t *testing.T) {
	cases := [][2]string{
		{"", ""},
		{"", "i"},
		{"i", ""},
		{"a", "b"},
		{"a", "a1"},
		{"0z", "1"},
		{"i", "j"},
		{"az", "b"},
	}
	for _, c := range cases {
		mid, err := rankBetween(c[0], c[1])
		if err != nil {
			t.Fatalf("rankBetween(%q, %q): %v", c[0], c[1], err)
		}
		if c[0] != "" && mid <= c[0] {
			t.Fatalf("rankBetween(%q, %q) = %q, not above prev", c[0], c[1], mid)
		}
		if c[1] != "" && mid >= c[1] {
			t.Fatalf("rankBetween(%q, %q) = %q, not below next", c[0], c[1], mid)
		}
		if strings.HasSuffix(mid, string(rankDigits[0])) {
			t.Fatalf("rankBetween(%q, %q) = %q ends with the zero digit", c[0], c[1], mid)
		}
	}
}

func TestRankBetweenRejectsBadOrder(t *testing.T) {
	if _, err := rankBetween("b", "a"); err == nil {
		t.Fatal("expected error for prev > next")
	}
	if _, err := rankBetween("a", "a"); err == nil {
		t.Fatal("expected error for prev == next")
	}
}

// Pathological insertion at the same gap: order must hold, growth must be
// roughly linear (this is exactly the degeneration rankNeedsRebalance
// exists to catch).
func TestRankRepeatedFrontInsertion(t *testing.T) {
	upper := rankInitial()
	prev := ""
	for range 100 {
		mid, err := rankBetween(prev, upper)
		if err != nil {
			t.Fatalf("rankBetween(%q, %q): %v", prev, upper, err)
		}
		if mid <= prev || mid >= upper {
			t.Fatalf("midpoint %q escaped (%q, %q)", mid, prev, upper)
		}
		prev = mid
	}
	if !rankNeedsRebalance(strings.Repeat("0", maxRankLength) + "1") {
		t.Fatal("over-length rank not flagged for rebalance")
	}
}

func TestRebalancedRanks(t *testing.T) {
	for _, n := range []int{0, 1, 2, 5, 100, 1000} {
		ranks := rebalancedRanks(n)
		if len(ranks) != n {
			t.Fatalf("rebalancedRanks(%d) returned %d keys", n, len(ranks))
		}
		if !sort.StringsAreSorted(ranks) {
			t.Fatalf("rebalancedRanks(%d) not sorted", n)
		}
		for i, r := range ranks {
			if i > 0 && ranks[i-1] >= r {
				t.Fatalf("rebalancedRanks(%d): duplicate/reversed at %d", n, i)
			}
			if strings.HasSuffix(r, string(rankDigits[0])) {
				t.Fatalf("rebalanced key %q ends with the zero digit", r)
			}
		}
		// A midpoint must fit between every adjacent pair post-rebalance.
		for i := 1; i < len(ranks); i++ {
			if _, err := rankBetween(ranks[i-1], ranks[i]); err != nil {
				t.Fatalf("no midpoint between %q and %q: %v", ranks[i-1], ranks[i], err)
			}
		}
	}
}
