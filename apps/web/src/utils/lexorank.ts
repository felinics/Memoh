// Client-side lexorank midpoint, mirroring the backend algorithm
// (internal/project/rank.go). The kanban drop handler computes the dragged
// card's new rank between its neighbors so one PATCH carries status + rank
// atomically; the server never needs a follow-up reorder call.
//
// Keys are base-36 fraction digits; '' stands for the open interval ends;
// no key ever ends with the zero digit — that invariant keeps a midpoint
// insertable below any existing key.

const RANK_DIGITS = '0123456789abcdefghijklmnopqrstuvwxyz'

function digitOrZero(s: string, i: number): string {
  return i < s.length ? s[i]! : RANK_DIGITS[0]!
}

function midpoint(a: string, b: string): string {
  if (b !== '') {
    let n = 0
    while (n < b.length && digitOrZero(a, n) === b[n]) n++
    if (n > 0) return b.slice(0, n) + midpoint(a.slice(n), b.slice(n))
  }

  const digitA = a === '' ? 0 : RANK_DIGITS.indexOf(a[0]!)
  const digitB = b === '' ? RANK_DIGITS.length : RANK_DIGITS.indexOf(b[0]!)
  if (digitA < 0 || digitB < 0) throw new Error(`invalid rank digit in ${JSON.stringify([a, b])}`)

  if (digitB - digitA > 1) {
    return RANK_DIGITS[Math.floor((digitA + digitB) / 2)]!
  }
  if (b.length > 1) {
    return b.slice(0, 1)
  }
  return RANK_DIGITS[digitA]! + midpoint(a.slice(1), '')
}

/**
 * A key strictly between prev and next. Pass '' for an open end:
 * rankBetween('', first) prepends, rankBetween(last, '') appends,
 * rankBetween('', '') seeds an empty group.
 */
export function rankBetween(prev: string, next: string): string {
  if (next !== '' && prev >= next) {
    throw new Error(`rank bounds out of order: ${JSON.stringify(prev)} >= ${JSON.stringify(next)}`)
  }
  return midpoint(prev, next)
}
