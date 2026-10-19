// Chat messages mix math with currency: a bare `$...$` pair around a price
// range ("US$3–6/月") is ambiguous, and markstream's parser resolves the
// ambiguity toward inline math — mangling prices into KaTeX spans that also
// overflow narrow table cells. Chat therefore treats a single `$` as never
// opening math: `$$...$$`, `\(...\)` and `\[...\]` keep rendering as math.
//
// The transform masks single `$` characters with a private-use placeholder at
// markdown-it inline-parse time and restores them afterwards. String-level
// `\$` escaping is deliberately not used: stream-markdown-parser drops a
// `\(...\)` when an escaped `\$` follows it in the same inline content, which
// is exactly the shape the system prompt now steers models toward.
//
// This is chat-only (message-item.vue). File preview deliberately keeps
// `$...$` math because user-authored .md notes use it.

import type MarkdownIt from 'markdown-it'

// Private-use placeholder swapped in for every single `$` before inline rules
// run, then swapped back after. Chosen from the PUA so it cannot collide with
// ordinary message content.
const PH = '\uE000'

const MASK_RULE = 'memoh_dollar_mask'
const RESTORE_RULE = 'memoh_dollar_restore'

interface MarkdownItCoreRuler {
  before: (beforeName: string, ruleName: string, rule: MarkdownIt.RuleCore) => void
  after: (afterName: string, ruleName: string, rule: MarkdownIt.RuleCore) => void
  // markdown-it's Ruler exposes __rules__; markstream's fork exposes rules.
  __rules__?: { name: string }[]
  rules?: { name: string }[]
}

interface MarkdownItCoreState {
  tokens: MarkdownIt.Token[]
}

/**
 * Replace every single `$` in inline content with PH, leaving `$$` runs,
 * inline code spans, and non-dollar backslash escapes untouched.
 */
export function maskSingleDollars(content: string): string {
  const out: string[] = []
  let i = 0
  while (i < content.length) {
    const ch = content[i]
    if (ch === '`') {
      let n = 1
      while (content[i + n] === '`') n++
      // CommonMark: the closing run must be exactly n backticks — not a
      // longer run whose first n ticks would fake a close.
      const run = '`'.repeat(n)
      let close = -1
      for (let j = i + n; ;) {
        j = content.indexOf(run, j)
        if (j < 0) break
        if (content[j - 1] !== '`' && content[j + n] !== '`') { close = j; break }
        j += n
      }
      if (close >= 0) {
        out.push(content.slice(i, close + n))
        i = close + n
      }
      else {
        out.push(run)
        i += n
      }
      continue
    }
    if (ch === '\\') {
      const next = content[i + 1]
      if (next === '$' && content[i + 2] !== '$') {
        // `\$` is the author's own escape: keep the literal dollar but drop
        // the backslash so the parser's inline-math rule never sees a `\$`.
        out.push(PH)
        i += 2
      }
      else {
        out.push(content.slice(i, i + 2))
        i += 2
      }
      continue
    }
    if (ch === '$') {
      let n = 1
      while (content[i + n] === '$') n++
      if (n >= 2) out.push(content.slice(i, i + n))
      else out.push(PH)
      i += n
      continue
    }
    out.push(ch)
    i++
  }
  return out.join('')
}

function restoreToken(token: MarkdownIt.Token, replacement: string): void {
  if (token.content) token.content = token.content.split(PH).join(replacement)
  if (typeof (token as { raw?: unknown }).raw === 'string') {
    ;(token as { raw: string }).raw = (token as { raw: string }).raw.split(PH).join(replacement)
  }
  if (token.attrs) {
    for (let i = 0; i < token.attrs.length; i++) {
      const pair = token.attrs[i]
      if (pair && typeof pair[1] === 'string') pair[1] = pair[1].split(PH).join(replacement)
    }
  }
}

function walkTokens(tokens: MarkdownIt.Token[], visit: (token: MarkdownIt.Token) => void): void {
  for (const token of tokens) {
    visit(token)
    if (token.children?.length) walkTokens(token.children, visit)
  }
}

/**
 * MarkdownIt customizer for markstream-vue's `customMarkdownIt` prop: masks
 * single `$` before the inline pass so the parser's math rule never sees
 * them, then restores them — as `$` in text and `\$` (a TeX literal dollar)
 * inside real math tokens.
 */
export function withLiteralSingleDollars(md: MarkdownIt): MarkdownIt {
  const ruler = md.core.ruler as unknown as MarkdownItCoreRuler
  const registered = (ruler.__rules__ ?? ruler.rules)?.some(rule => rule.name === MASK_RULE)
  if (registered) return md

  ruler.before('inline', MASK_RULE, (state: MarkdownItCoreState) => {
    for (const token of state.tokens) {
      if (token.type === 'inline') token.content = maskSingleDollars(token.content)
    }
    return false
  })
  ruler.after('inline', RESTORE_RULE, (state: MarkdownItCoreState) => {
    walkTokens(state.tokens, (token) => {
      restoreToken(token, token.type === 'math_inline' || token.type === 'math_block' ? '\\$' : '$')
    })
    return false
  })
  return md
}
