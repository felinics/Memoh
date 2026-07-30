// Let the browser/platform choose the concrete system fonts.
export const DEFAULT_UI_FONT_FAMILY = 'system-ui, sans-serif'
export const DEFAULT_CODE_FONT_FAMILY = 'ui-monospace, monospace'
export const DEFAULT_UI_FONT_SIZE_PX = 16
export const DEFAULT_CODE_FONT_SIZE_PX = 13

// CJK concrete families inserted before the terminal generic monospace on EVERY
// code stack (default or user-custom). The Latin mono families ahead carry no CJK
// members, so without MiSans the glyphs fall to whatever per-lang font the OS
// picks — under <html lang="zh"> the :lang(zh) weight shave (style.css) drops
// the request to ~315, which lands on PingFang's Light static face and renders
// visibly thin/blurry at 12–13px (#851). MiSans leads the CJK run so code zh
// matches the rest of the UI; system CJK fonts in this list are only for glyphs
// outside MiSans' subset ranges. Latin is always claimed earlier by the mono
// families, and MiSans' unicode-range subsets claim no color-emoji glyphs, so
// neither Latin nor emoji rendering changes. The CSS fallback lists in
// style.css (var(--memoh-code-font-family, …)) mirror this ordering — keep
// them in sync.
export const CODE_FONT_CJK_FALLBACK = 'MiSans, PingFang SC, Hiragino Sans GB, Microsoft YaHei UI, Noto Sans SC'

const MIN_UI_FONT_SIZE_PX = 12
const MAX_UI_FONT_SIZE_PX = 20
const MIN_CODE_FONT_SIZE_PX = 11
const MAX_CODE_FONT_SIZE_PX = 20
const CSS_GENERIC_FONT_FAMILIES = new Set([
  'serif',
  'sans-serif',
  'monospace',
  'cursive',
  'fantasy',
  'system-ui',
  'ui-serif',
  'ui-sans-serif',
  'ui-monospace',
  'ui-rounded',
  'emoji',
  'math',
  'fangsong',
])

function normalizePx(value: unknown, fallback: number, min: number, max: number): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) return fallback
  return Math.min(max, Math.max(min, Math.round(parsed)))
}

export function normalizeUiFontSizePx(value: unknown): number {
  return normalizePx(value, DEFAULT_UI_FONT_SIZE_PX, MIN_UI_FONT_SIZE_PX, MAX_UI_FONT_SIZE_PX)
}

export function normalizeCodeFontSizePx(value: unknown): number {
  return normalizePx(value, DEFAULT_CODE_FONT_SIZE_PX, MIN_CODE_FONT_SIZE_PX, MAX_CODE_FONT_SIZE_PX)
}

const MAX_FONT_FAMILY_INPUT_LENGTH = 256

function truncateFontFamilyInput(value: string): string {
  let truncated = value.slice(0, MAX_FONT_FAMILY_INPUT_LENGTH)
  // Don't leave a lone high surrogate behind when the cut lands inside a
  // surrogate pair.
  const lastCode = truncated.charCodeAt(truncated.length - 1)
  if (lastCode >= 0xD800 && lastCode <= 0xDBFF) {
    truncated = truncated.slice(0, -1)
  }
  return truncated
}

function hasUnterminatedQuote(family: string): boolean {
  const first = family.at(0)
  return (first === '"' || first === '\'') && (family.length < 2 || family.at(-1) !== first)
}

export function normalizeFontFamilyInput(value: unknown): string {
  if (typeof value !== 'string') return ''
  const normalized = truncateFontFamilyInput(value)
    .replace(/[\r\n\f]+/g, ' ')
    .trim()
    .replace(/^[;,]+/g, '')
    .replace(/[;,]+$/g, '')
    .trim()
  const families = splitFontFamilyList(normalized)
    .map((family) => family.trim().replace(/^[;]+|[;]+$/g, '').trim())
    // Truncation can cut the input inside a quoted family name; drop such
    // garbage tails instead of letting them swallow the rest of the stack.
    .filter((family) => family && !hasUnterminatedQuote(family))

  // Separator rewriting (`;` -> `, `) can grow the string again, so re-apply
  // the cap on whole families to keep the stored value bounded.
  const result: string[] = []
  let length = 0
  for (const family of families) {
    const nextLength = length + family.length + (result.length > 0 ? 2 : 0)
    if (result.length > 0 && nextLength > MAX_FONT_FAMILY_INPUT_LENGTH) break
    result.push(family)
    length = nextLength
  }
  return result.join(', ')
}

export function normalizeStoredFontFamilyInput(value: unknown, fallback: string): string {
  const fontFamily = normalizeFontFamilyInput(value).trim()
  const normalizedFontFamily = fontFamily.toLowerCase()
  if (!fontFamily || normalizedFontFamily === fallback.toLowerCase() || normalizedFontFamily === 'default') {
    return ''
  }
  return fontFamily
}

function isGenericFontFamily(family: string): boolean {
  const trimmed = family.trim()
  const first = trimmed.at(0)
  // Quoted names are concrete font names even when they spell a generic
  // keyword, so only bare identifiers count.
  if (first === '"' || first === '\'') return false
  return CSS_GENERIC_FONT_FAMILIES.has(trimmed.toLowerCase())
}

export function cssFontStack(value: unknown, fallback: string): string {
  const fontFamily = normalizeStoredFontFamilyInput(value, fallback)
  if (!fontFamily) return fallback
  // Append the default fallback stack unless the user stack already ends in a
  // generic family that guarantees a match. The quote-aware splitter keeps a
  // single quoted family containing a comma (e.g. "Foo, Bar") as one family.
  if (splitFontFamilyList(fontFamily).some(isGenericFontFamily)) return fontFamily
  return `${fontFamily}, ${fallback}`
}

export function cssFontFamilyDeclaration(value: unknown, fallback: string): string {
  return cssFontFamilyStyleValue(value, fallback)
}

// Code stacks only: Latin mono families first, then the CJK concrete stack, then
// a terminal generic monospace catch-all. CJK is inserted BEFORE that final
// monospace so browsers don't satisfy zh glyphs via the OS monospace pick
// (PingFang Light under :lang(zh) weight shave — #851) before MiSans is tried.
// Goes through cssFontStack first so user/custom Latin families stay ahead of
// the default ui-monospace fallback, and CJK is still injected when the user
// stack already ends in a generic family (where cssFontStack appends nothing).
export function cssCodeFontFamilyStyleValue(value: unknown): string {
  const baseFamilies = splitFontFamilyList(
    cssFontFamilyStyleValue(value, DEFAULT_CODE_FONT_FAMILY),
  )
  if (baseFamilies.length > 0 && baseFamilies.at(-1)!.toLowerCase() === 'monospace') {
    baseFamilies.pop()
  }
  const head = baseFamilies.map((family) =>
    serializeFontFamily(family, DEFAULT_CODE_FONT_FAMILY),
  )
  const cjk = splitFontFamilyList(CODE_FONT_CJK_FALLBACK).map((family) =>
    serializeFontFamily(family, 'monospace'),
  )
  return [...head, ...cjk, 'monospace'].join(', ')
}

export function cssFontFamilyStyleValue(value: unknown, fallback: string): string {
  return splitFontFamilyList(cssFontStack(value, fallback))
    .map((family) => serializeFontFamily(family, fallback))
    .join(', ')
}

function splitFontFamilyList(value: string): string[] {
  const families: string[] = []
  let current = ''
  let quote: '"' | '\'' | null = null
  let escaped = false

  for (const char of value) {
    if (escaped) {
      current += char
      escaped = false
      continue
    }

    if (char === '\\') {
      current += char
      escaped = true
      continue
    }

    if ((char === '"' || char === '\'') && !quote) {
      quote = char
      current += char
      continue
    }

    if (char === quote) {
      quote = null
      current += char
      continue
    }

    if ((char === ',' || char === ';') && !quote) {
      const family = current.trim()
      if (family) families.push(family)
      current = ''
      continue
    }

    current += char
  }

  const family = current.trim()
  if (family) families.push(family)
  return families
}

function serializeFontFamily(value: string, fallback: string): string {
  const unquoted = stripMatchingQuotes(value.trim())
  if (!unquoted) return fallback
  const normalized = unquoted.toLowerCase()
  if (CSS_GENERIC_FONT_FAMILIES.has(normalized)) return normalized
  return `"${escapeCssString(unquoted)}"`
}

function stripMatchingQuotes(value: string): string {
  if (value.length < 2) return value
  const first = value.at(0)
  const last = value.at(-1)
  return (first === last && (first === '"' || first === '\''))
    ? value.slice(1, -1)
    : value
}

function escapeCssString(value: string): string {
  // `<` is escaped so the serialized value can never form a literal
  // `</style>` when interpolated into inline <style> text (html-preview).
  return value
    .replace(/\\/g, '\\\\')
    .replace(/"/g, '\\"')
    .replace(/\n/g, '\\a ')
    .replace(/\r/g, '\\d ')
    .replace(/\f/g, '\\c ')
    .replace(/</g, '\\3c ')
}

export function applyTypographyVariables(options: {
  uiFontFamily: string
  codeFontFamily: string
  uiFontSizePx: number
  codeFontSizePx: number
}) {
  if (typeof document === 'undefined') return

  const uiSize = normalizeUiFontSizePx(options.uiFontSizePx)
  const codeSize = normalizeCodeFontSizePx(options.codeFontSizePx)
  const uiFamily = normalizeStoredFontFamilyInput(options.uiFontFamily, DEFAULT_UI_FONT_FAMILY)
  const codeFamily = normalizeStoredFontFamilyInput(options.codeFontFamily, DEFAULT_CODE_FONT_FAMILY)
  const style = document.documentElement.style

  style.setProperty('--memoh-ui-font-family', cssFontFamilyDeclaration(options.uiFontFamily, DEFAULT_UI_FONT_FAMILY))
  style.setProperty('--memoh-code-font-family', cssCodeFontFamilyStyleValue(options.codeFontFamily))

  // Tailwind's preflight resolves body/code fonts through --font-sans /
  // --font-mono at runtime. Override them only for explicit customizations so
  // the default render stays exactly Tailwind's stock stacks.
  if (uiFamily) {
    style.setProperty('--font-sans', cssFontFamilyDeclaration(uiFamily, DEFAULT_UI_FONT_FAMILY))
  } else {
    style.removeProperty('--font-sans')
  }
  if (codeFamily) {
    style.setProperty('--font-mono', cssCodeFontFamilyStyleValue(codeFamily))
  } else {
    style.removeProperty('--font-mono')
  }

  // Only pin the root font-size when the user explicitly deviates from the
  // default, so browser/OS accessibility font-size settings keep working.
  if (uiSize === DEFAULT_UI_FONT_SIZE_PX) {
    style.removeProperty('--memoh-ui-font-size')
  } else {
    style.setProperty('--memoh-ui-font-size', `${uiSize}px`)
  }
  style.setProperty('--memoh-code-font-size', `${codeSize}px`)
}
