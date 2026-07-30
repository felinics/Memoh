import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  applyTypographyVariables,
  cssCodeFontFamilyStyleValue,
  cssFontFamilyDeclaration,
  cssFontFamilyStyleValue,
  cssFontStack,
  DEFAULT_CODE_FONT_FAMILY,
  DEFAULT_UI_FONT_FAMILY,
  normalizeFontFamilyInput,
  normalizeCodeFontSizePx,
  normalizeStoredFontFamilyInput,
  normalizeUiFontSizePx,
} from './typography'

// The default stacks after serialization (generic keywords stay bare, concrete
// names get quoted).
const UI_FALLBACK_SERIALIZED = 'system-ui, sans-serif'
// CJK concrete families sit before the terminal generic monospace catch-all (#851).
const CODE_CJK_CORE_SERIALIZED = '"MiSans", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei UI", "Noto Sans SC"'
const CODE_STACK_WITH_CJK = `ui-monospace, ${CODE_CJK_CORE_SERIALIZED}, monospace`

describe('typography settings', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('normalizes UI and code font sizes to supported ranges', () => {
    expect(normalizeUiFontSizePx(10)).toBe(12)
    expect(normalizeUiFontSizePx(30)).toBe(20)
    expect(normalizeUiFontSizePx(17)).toBe(17)
    expect(normalizeCodeFontSizePx(10)).toBe(11)
    expect(normalizeCodeFontSizePx(30)).toBe(20)
    expect(normalizeCodeFontSizePx(18)).toBe(18)
  })

  it('falls back to defaults for empty, zero, or negative sizes', () => {
    expect(normalizeUiFontSizePx('')).toBe(16)
    expect(normalizeUiFontSizePx(0)).toBe(16)
    expect(normalizeUiFontSizePx(-5)).toBe(16)
    expect(normalizeCodeFontSizePx('')).toBe(13)
    expect(normalizeCodeFontSizePx(0)).toBe(13)
    expect(normalizeCodeFontSizePx(-5)).toBe(13)
  })

  it('serializes font families safely for style text', () => {
    expect(cssFontFamilyStyleValue('Inter, system-ui, sans-serif', DEFAULT_UI_FONT_FAMILY))
      .toBe('"Inter", system-ui, sans-serif')
    expect(cssFontFamilyStyleValue('ui-monospace, "SF Mono", monospace', DEFAULT_CODE_FONT_FAMILY))
      .toBe('ui-monospace, "SF Mono", monospace')
    expect(cssFontFamilyStyleValue('x:/**/url(https://example.test/font)', DEFAULT_UI_FONT_FAMILY))
      .toBe(`"x:/**/url(https://example.test/font)", ${UI_FALLBACK_SERIALIZED}`)
  })

  it('keeps commas inside quoted font family names', () => {
    expect(cssFontFamilyStyleValue('"Foo, Bar", ui-monospace, monospace', DEFAULT_CODE_FONT_FAMILY))
      .toBe('"Foo, Bar", ui-monospace, monospace')
  })

  it('appends the fallback stack when no generic family is present', () => {
    expect(cssFontStack('"Foo, Bar"', DEFAULT_CODE_FONT_FAMILY))
      .toBe(`"Foo, Bar", ${DEFAULT_CODE_FONT_FAMILY}`)
    expect(cssFontStack('Inter, Arial', DEFAULT_UI_FONT_FAMILY))
      .toBe(`Inter, Arial, ${DEFAULT_UI_FONT_FAMILY}`)
    // A stack that already contains a generic family is kept untouched.
    expect(cssFontStack('Inter, sans-serif', DEFAULT_UI_FONT_FAMILY))
      .toBe('Inter, sans-serif')
  })

  it('inserts the CJK stack before terminal monospace on every code stack (#851)', () => {
    expect(cssCodeFontFamilyStyleValue('')).toBe(CODE_STACK_WITH_CJK)
    expect(cssCodeFontFamilyStyleValue('JetBrains Mono'))
      .toBe(`"JetBrains Mono", ${CODE_STACK_WITH_CJK}`)
    // User stacks ending in generic monospace still get CJK ahead of the catch-all.
    expect(cssCodeFontFamilyStyleValue('JetBrains Mono, monospace'))
      .toBe(`"JetBrains Mono", ${CODE_CJK_CORE_SERIALIZED}, monospace`)
  })

  it('normalizes free-text font family input before storing it', () => {
    expect(normalizeFontFamilyInput('  Inter,\n')).toBe('Inter')
    expect(normalizeFontFamilyInput('SF Mono;  ')).toBe('SF Mono')
    expect(normalizeFontFamilyInput('Foo\nBar, baz')).toBe('Foo Bar, baz')
    expect(normalizeFontFamilyInput(',, Inter,, sans-serif;;')).toBe('Inter, sans-serif')
    expect(normalizeFontFamilyInput(',"Foo, Bar",, ui-monospace,')).toBe('"Foo, Bar", ui-monospace')
    expect(normalizeFontFamilyInput('Inter; Arial')).toBe('Inter, Arial')
    expect(normalizeFontFamilyInput('"Foo; Bar", sans-serif')).toBe('"Foo; Bar", sans-serif')
  })

  it('stores only explicit custom font families', () => {
    expect(normalizeStoredFontFamilyInput('', DEFAULT_UI_FONT_FAMILY)).toBe('')
    expect(normalizeStoredFontFamilyInput('   ', DEFAULT_UI_FONT_FAMILY)).toBe('')
    expect(normalizeStoredFontFamilyInput('default', DEFAULT_UI_FONT_FAMILY)).toBe('')
    expect(normalizeStoredFontFamilyInput(DEFAULT_UI_FONT_FAMILY, DEFAULT_UI_FONT_FAMILY)).toBe('')
    expect(normalizeStoredFontFamilyInput(DEFAULT_UI_FONT_FAMILY.toUpperCase(), DEFAULT_UI_FONT_FAMILY)).toBe('')
    expect(normalizeStoredFontFamilyInput('Inter', DEFAULT_UI_FONT_FAMILY)).toBe('Inter')
  })

  it('uses safe serialization for app-wide font CSS variables', () => {
    expect(cssFontFamilyDeclaration('x:/**/url(https://example.test/font)', DEFAULT_UI_FONT_FAMILY))
      .toBe(`"x:/**/url(https://example.test/font)", ${UI_FALLBACK_SERIALIZED}`)
  })

  it('escapes < so serialized families cannot break out of inline style text', () => {
    expect(cssFontFamilyStyleValue('</style><b>x', DEFAULT_UI_FONT_FAMILY))
      .toBe(`"\\3c /style>\\3c b>x", ${UI_FALLBACK_SERIALIZED}`)
  })

  it('truncates excessively long font family input', () => {
    expect(normalizeFontFamilyInput(`Custom Font${'x'.repeat(10_000)}`).length)
      .toBeLessThanOrEqual(256)
    // Separator rewriting (`;` -> `, `) must not grow the result past the cap.
    expect(normalizeFontFamilyInput('ab;'.repeat(1_000)).length)
      .toBeLessThanOrEqual(256)
  })

  it('drops a quoted family left unterminated by truncation', () => {
    const truncatedQuote = `Inter, "Custom ${'x'.repeat(10_000)}`
    expect(normalizeFontFamilyInput(truncatedQuote)).toBe('Inter')
    expect(normalizeFontFamilyInput('"Unterminated')).toBe('')
  })

  it('writes only the app-wide typography CSS variables', () => {
    const properties = new Map<string, string>()
    vi.stubGlobal('document', {
      documentElement: {
        style: {
          setProperty: (name: string, value: string) => properties.set(name, value),
          removeProperty: (name: string) => properties.delete(name),
        },
      },
    })

    applyTypographyVariables({
      uiFontFamily: 'A "Quoted" Font',
      codeFontFamily: 'Mono\\Font',
      uiFontSizePx: 14,
      codeFontSizePx: 13,
    })

    expect(properties.get('--memoh-ui-font-family')).toBe(`"A \\"Quoted\\" Font", ${UI_FALLBACK_SERIALIZED}`)
    expect(properties.get('--font-sans')).toBe(`"A \\"Quoted\\" Font", ${UI_FALLBACK_SERIALIZED}`)
    expect(properties.get('--memoh-ui-font-size')).toBe('14px')
    expect(properties.get('--memoh-code-font-family')).toBe(cssCodeFontFamilyStyleValue('Mono\\Font'))
    expect(properties.get('--font-mono')).toBe(cssCodeFontFamilyStyleValue('Mono\\Font'))
    expect(properties.get('--memoh-code-font-size')).toBe('13px')
    expect(properties.has('--memoh-text-xs')).toBe(false)
    expect(properties.has('--chat-markdown-h1-font-size')).toBe(false)
    expect(properties.has('--memoh-markdown-h1-font-size')).toBe(false)
  })

  it('keeps Tailwind defaults untouched when nothing is customized', () => {
    const properties = new Map<string, string>()
    vi.stubGlobal('document', {
      documentElement: {
        style: {
          setProperty: (name: string, value: string) => properties.set(name, value),
          removeProperty: (name: string) => properties.delete(name),
        },
      },
    })

    applyTypographyVariables({
      uiFontFamily: 'Inter',
      codeFontFamily: 'My Mono',
      uiFontSizePx: 18,
      codeFontSizePx: 14,
    })
    expect(properties.has('--font-sans')).toBe(true)
    expect(properties.has('--font-mono')).toBe(true)
    expect(properties.get('--memoh-ui-font-size')).toBe('18px')

    applyTypographyVariables({
      uiFontFamily: '',
      codeFontFamily: '',
      uiFontSizePx: 16,
      codeFontSizePx: 13,
    })
    expect(properties.has('--font-sans')).toBe(false)
    expect(properties.has('--font-mono')).toBe(false)
    expect(properties.has('--memoh-ui-font-size')).toBe(false)
    expect(properties.get('--memoh-ui-font-family')).toBe(UI_FALLBACK_SERIALIZED)
    expect(properties.get('--memoh-code-font-family')).toBe(CODE_STACK_WITH_CJK)
  })
})
