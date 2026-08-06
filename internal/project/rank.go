package project

import (
	"errors"
	"fmt"
	"strings"
)

// Lexorank-style ordering keys. Both the doc tree and the kanban columns
// order rows by a plain string key so a drag updates exactly one row.
//
// The midpoint algorithm is the fractional-indexing scheme (rocicorp):
// keys are base-36 fraction digits, "" stands for the open interval ends,
// and no key ever ends with the zero digit — that invariant is what keeps
// a midpoint insertable below any existing key.

const rankDigits = "0123456789abcdefghijklmnopqrstuvwxyz"

// maxRankLength is the rebalance trigger. Repeated insertion between two
// adjacent keys grows the key by ~1 digit per insertion; hitting this many
// digits means that sibling group needs re-spreading.
const maxRankLength = 64

var errRankOrder = errors.New("rank bounds out of order")

// rankInitial returns the key for the first element of an empty group.
func rankInitial() string {
	return string(rankDigits[len(rankDigits)/2])
}

// rankAfter returns a key sorting after every existing key in the group.
// Pass the current maximum ("" for an empty group).
//
// Append is the hot path, so instead of a pure midpoint (which grows the
// key every few appends) it bumps the first bumpable digit and drops the
// tail — the key only lengthens once the whole prefix is maxed out, i.e.
// every ~35 appends per digit. Rebalance remains the backstop.
func rankAfter(last string) (string, error) {
	if last == "" {
		return rankInitial(), nil
	}
	for i := range len(last) {
		d := strings.IndexByte(rankDigits, last[i])
		if d < 0 {
			return "", fmt.Errorf("invalid rank digit %q", last[i])
		}
		if d < len(rankDigits)-1 {
			return last[:i] + string(rankDigits[d+1]), nil
		}
	}
	// Entirely 'z': extend with the lowest non-zero digit.
	return last + string(rankDigits[1]), nil
}

// rankBetween returns a key strictly between prev and next. "" for prev
// means the low end of the interval, "" for next means the high end.
func rankBetween(prev, next string) (string, error) {
	if next != "" && prev >= next {
		return "", fmt.Errorf("%w: %q >= %q", errRankOrder, prev, next)
	}
	return midpoint(prev, next)
}

func midpoint(a, b string) (string, error) {
	if b != "" {
		// Shared prefix recurses into the differing suffix.
		n := 0
		for n < len(b) && digitOrZero(a, n) == b[n] {
			n++
		}
		if n > 0 {
			suffix, err := midpoint(sliceFrom(a, n), b[n:])
			if err != nil {
				return "", err
			}
			return b[:n] + suffix, nil
		}
	}

	digitA := 0
	if a != "" {
		digitA = strings.IndexByte(rankDigits, a[0])
		if digitA < 0 {
			return "", fmt.Errorf("invalid rank digit %q", a[0])
		}
	}
	digitB := len(rankDigits)
	if b != "" {
		digitB = strings.IndexByte(rankDigits, b[0])
		if digitB < 0 {
			return "", fmt.Errorf("invalid rank digit %q", b[0])
		}
	}

	if digitB-digitA > 1 {
		mid := (digitA + digitB) / 2
		return string(rankDigits[mid]), nil
	}

	// Adjacent leading digits: descend along a's side of the gap.
	if len(b) > 1 {
		return b[:1], nil
	}
	suffix, err := midpoint(sliceFrom(a, 1), "")
	if err != nil {
		return "", err
	}
	return string(rankDigits[digitA]) + suffix, nil
}

func digitOrZero(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return rankDigits[0]
}

func sliceFrom(s string, i int) string {
	if i < len(s) {
		return s[i:]
	}
	return ""
}

// rankNeedsRebalance reports whether a freshly computed key signals that
// its sibling group has degenerated.
func rankNeedsRebalance(rank string) bool {
	return len(rank) > maxRankLength
}

// rebalancedRanks produces n short, evenly spaced keys for re-spreading a
// sibling group. Values are (i+1)*36² + 1 rendered in fixed-width base 36:
// the spacing leaves ~1200 gaps between neighbors and the +1 guarantees no
// key ends with the zero digit.
func rebalancedRanks(n int) []string {
	if n <= 0 {
		return nil
	}
	const step = 36 * 36
	width := base36Width(n*step + 1)
	ranks := make([]string, n)
	for i := range n {
		ranks[i] = toBase36(i*step+step+1, width)
	}
	return ranks
}

func base36Width(v int) int {
	width := 1
	for v >= len(rankDigits) {
		v /= len(rankDigits)
		width++
	}
	return width
}

func toBase36(v, width int) string {
	buf := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		buf[i] = rankDigits[v%len(rankDigits)]
		v /= len(rankDigits)
	}
	return string(buf)
}
