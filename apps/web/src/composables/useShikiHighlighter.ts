import { ref } from 'vue'
import { getLanguageByFilename } from '@/components/file-manager/utils'
import { useSettingsStore } from '@/store/settings'

import type { HighlighterGeneric, BundledLanguage, BundledTheme } from 'shiki'

type Highlighter = HighlighterGeneric<BundledLanguage, BundledTheme>

let highlighterPromise: Promise<Highlighter> | null = null
const loadedLangs = new Set<string>(['plaintext'])
const loadedThemes = new Set<string>()

async function getHighlighter(): Promise<Highlighter> {
  if (!highlighterPromise) {
    highlighterPromise = import('shiki').then((m) =>
      m.createHighlighter({ themes: [], langs: [] }),
    )
  }
  return highlighterPromise
}

async function ensureLang(hl: Highlighter, lang: string) {
  if (loadedLangs.has(lang)) return
  try {
    await hl.loadLanguage(lang as BundledLanguage)
    loadedLangs.add(lang)
  } catch {
    loadedLangs.add(lang)
  }
}

async function ensureTheme(hl: Highlighter, theme: BundledTheme) {
  if (loadedThemes.has(theme)) return
  try {
    await hl.loadTheme(theme)
    loadedThemes.add(theme)
  } catch {
    loadedThemes.add(theme)
  }
}

export function useShikiHighlighter() {
  const settings = useSettingsStore()
  const html = ref('')
  const diffRows = ref<ContextDiffRow[]>([])
  const loading = ref(false)

  const activeThemes = () => ({
    light: settings.shikiThemeLight as BundledTheme,
    dark: settings.shikiThemeDark as BundledTheme,
  })

  async function ensurePairedThemes(hl: Highlighter) {
    const themes = activeThemes()
    await Promise.all([ensureTheme(hl, themes.light), ensureTheme(hl, themes.dark)])
    return themes
  }

  async function highlight(code: string, filename: string) {
    loading.value = true
    try {
      const lang = getLanguageByFilename(filename)
      const hl = await getHighlighter()
      await ensureLang(hl, lang)
      const themes = await ensurePairedThemes(hl)
      html.value = hl.codeToHtml(code, {
        lang: loadedLangs.has(lang) ? lang : 'plaintext',
        themes,
      })
    } catch {
      html.value = `<pre>${escapeHtml(code)}</pre>`
    } finally {
      loading.value = false
    }
  }

  // Highlight by an explicit language id (markdown code fences carry the
  // language directly, e.g. ```bash), rather than deriving it from a filename.
  async function highlightLang(code: string, lang: string) {
    loading.value = true
    try {
      const normalized = (lang || 'plaintext').toLowerCase()
      const hl = await getHighlighter()
      await ensureLang(hl, normalized)
      const themes = await ensurePairedThemes(hl)
      html.value = hl.codeToHtml(code, {
        lang: loadedLangs.has(normalized) ? normalized : 'plaintext',
        themes,
      })
    } catch {
      html.value = `<pre>${escapeHtml(code)}</pre>`
    } finally {
      loading.value = false
    }
  }

  async function highlightDiff(oldText: string, newText: string, filename: string) {
    loading.value = true
    try {
      const lang = getLanguageByFilename(filename)
      const hl = await getHighlighter()
      await ensureLang(hl, lang)
      const themes = await ensurePairedThemes(hl)
      const effectiveLang = loadedLangs.has(lang) ? lang : 'plaintext'

      const oldHtml = oldText
        ? hl.codeToHtml(oldText, { lang: effectiveLang, themes })
        : ''
      const newHtml = newText
        ? hl.codeToHtml(newText, { lang: effectiveLang, themes })
        : ''

      html.value =
        (oldHtml ? `<div class="diff-block diff-remove">${oldHtml}</div>` : '') +
        (newHtml ? `<div class="diff-block diff-add">${newHtml}</div>` : '')
    } catch {
      html.value = `<pre>${escapeHtml(`- ${oldText}\n+ ${newText}`)}</pre>`
    } finally {
      loading.value = false
    }
  }

  // Render a server-computed unified diff as a VS Code-style read-only diff
  // panel: gutter line numbers (old numbers for removals, new for
  // additions/context), a −/+ marker column, whole-row red/green bands, and
  // an inline emphasis block on the exact replaced fragments (computed as the
  // span between the paired lines' common prefix and suffix — the same span
  // stays highlighted even where its words also appear in the other line).
  // Code is syntax-highlighted with the file's language; ---/+++ headers and
  // @@ hunk lines never show (the hunk headers only feed the line counters).
  async function highlightContextDiff(diffText: string, filename: string) {
    loading.value = true
    try {
      const parsed = parseUnifiedDiffRows(diffText)
      const lang = getLanguageByFilename(filename)
      const hl = await getHighlighter()
      await ensureLang(hl, lang)
      const themes = await ensurePairedThemes(hl)
      const effectiveLang = loadedLangs.has(lang) ? lang : 'plaintext'

      // Highlight the displayed lines as one contiguous code block so
      // multi-line constructs (comments, template strings) color correctly,
      // then re-wrap each line with its diff role. Decorations attach the
      // inline-emphasis class without disturbing the token spans; alwaysWrap
      // forces a wrapper span even when a decoration covers its whole line,
      // so shiki never promotes the class onto the line element itself (that
      // would break the per-line split below).
      const codeText = parsed.map((line) => line.text).join('\n')
      const inlineRanges = computeInlineRanges(parsed)
      const decorations = [...inlineRanges].map(([line, [start, end]]) => ({
        start: { line, character: start },
        end: { line, character: end },
        alwaysWrap: true,
        properties: { class: 'diff-inline' },
      }))
      const highlighted = hl.codeToHtml(codeText, {
        lang: effectiveLang,
        themes,
        decorations,
      })
      const lineHtml = splitShikiLineHtml(highlighted)
      if (lineHtml.length !== parsed.length) {
        throw new Error(`shiki line split mismatch: ${lineHtml.length} vs ${parsed.length}`)
      }

      diffRows.value = parsed.map((line, i) => ({
        kind: line.kind,
        lineNumber: line.lineNumber,
        html: lineHtml[i] ?? '',
      }))
    } catch {
      diffRows.value = []
      html.value = `<pre>${escapeHtml(diffText)}</pre>`
    } finally {
      loading.value = false
    }
  }

  async function highlightLanguage(code: string, lang: string, options: {
    theme?: BundledTheme
    // Explicit dual-theme override. Pass when the call site needs to dodge the
    // `.dark .shiki span` !important rule that ships in the design system: set
    // both halves to the same theme and shiki emits `--shiki-dark` equal to the
    // light value, so the override resolves back to the picked colors.
    themes?: { light: BundledTheme, dark: BundledTheme }
    transparentPre?: boolean
  } = {}) {
    loading.value = true
    try {
      const hl = await getHighlighter()
      await ensureLang(hl, lang)
      const effectiveLang = loadedLangs.has(lang) ? lang : 'plaintext'
      const transformers = options.transparentPre ? [transparentPreTransformer] : undefined
      if (options.theme) {
        await ensureTheme(hl, options.theme)
        html.value = hl.codeToHtml(code, {
          lang: effectiveLang,
          theme: options.theme,
          transformers,
        })
      } else if (options.themes) {
        await Promise.all([
          ensureTheme(hl, options.themes.light),
          ensureTheme(hl, options.themes.dark),
        ])
        html.value = hl.codeToHtml(code, {
          lang: effectiveLang,
          themes: options.themes,
          transformers,
        })
      } else {
        const themes = await ensurePairedThemes(hl)
        html.value = hl.codeToHtml(code, {
          lang: effectiveLang,
          themes,
          transformers,
        })
      }
    } catch {
      html.value = `<pre>${escapeHtml(code)}</pre>`
    } finally {
      loading.value = false
    }
  }

  return { html, diffRows, loading, highlight, highlightLang, highlightDiff, highlightContextDiff, highlightLanguage }
}

export type ContextDiffRowKind = 'context' | 'remove' | 'add'

// ContextDiffRow is one rendered line of the edit diff panel: its diff role,
// the gutter line number, and the shiki-highlighted inner HTML of the code.
export interface ContextDiffRow {
  kind: ContextDiffRowKind
  lineNumber: number
  html: string
}

interface ParsedDiffLine {
  kind: ContextDiffRowKind
  lineNumber: number
  text: string
}

// UnifiedDiffFile is one file's section of a multi-file unified diff, with
// the a/ and b/ header prefixes stripped from its paths.
export interface UnifiedDiffFile {
  oldPath: string
  newPath: string
  diff: string
}

const HUNK_HEADER = /^@@ -\d+(?:,(\d+))? \+\d+(?:,(\d+))? @@/

function diffHeaderPath(line: string, prefix: string): string {
  const path = line.slice(4)
  if (path === '/dev/null') return ''
  return path.startsWith(prefix) ? path.slice(prefix.length) : path
}

// splitUnifiedDiffFiles cuts a multi-file unified diff (apply_patch's UI diff)
// into per-file sections, since the diff panel and its row parser take one
// file. Hunk ends come from the line counts in each @@ header, not from
// spotting the next ---/+++ pair: a removed "-- x" line followed by an added
// "++ y" line looks exactly like a file header. Input that does not parse
// cleanly — wrong counts, text outside a hunk — yields [] so callers fall back
// to their raw view instead of rendering misattributed rows.
export function splitUnifiedDiffFiles(diffText: string): UnifiedDiffFile[] {
  const lines = diffText.split('\n')
  if (lines.at(-1) === '') lines.pop()
  const files: UnifiedDiffFile[] = []
  let i = 0
  while (i < lines.length) {
    const fromLine = lines[i]!
    const toLine = lines[i + 1]
    if (!fromLine.startsWith('--- ') || !toLine?.startsWith('+++ ')) return []
    const start = i
    i += 2
    while (i < lines.length && lines[i]!.startsWith('@@')) {
      const match = lines[i]!.match(HUNK_HEADER)
      if (!match) return []
      // An omitted count means one line.
      let oldLeft = match[1] === undefined ? 1 : Number(match[1])
      let newLeft = match[2] === undefined ? 1 : Number(match[2])
      i += 1
      while (oldLeft > 0 || newLeft > 0) {
        const line = lines[i]
        if (line === undefined) return []
        i += 1
        if (line.startsWith('\\')) continue
        if (line.startsWith('-')) oldLeft -= 1
        else if (line.startsWith('+')) newLeft -= 1
        else {
          oldLeft -= 1
          newLeft -= 1
        }
        if (oldLeft < 0 || newLeft < 0) return []
      }
      // The last line of a side may still carry its "\ No newline" marker.
      while (i < lines.length && lines[i]!.startsWith('\\')) i += 1
    }
    files.push({
      oldPath: diffHeaderPath(fromLine, 'a/'),
      newPath: diffHeaderPath(toLine, 'b/'),
      diff: lines.slice(start, i).join('\n'),
    })
  }
  return files
}

// parseUnifiedDiffRows drops the ---/+++ file headers (they duplicate the row
// title) and the "\ No newline" marker, strips the one-character prefix from
// content lines, and uses the @@ hunk headers to track the old/new line
// counters: removal rows show the old line number, addition and context rows
// the new one. Hunk header lines themselves are never rendered.
export function parseUnifiedDiffRows(diffText: string): ParsedDiffLine[] {
  const rows: ParsedDiffLine[] = []
  let oldLine = 1
  let newLine = 1
  let seenHunk = false
  // A trailing newline would split into a phantom empty content line; real
  // content lines always carry their prefix, so only the tail can be bare.
  const rawLines = diffText.split('\n')
  if (rawLines.at(-1) === '') rawLines.pop()
  for (const raw of rawLines) {
    if (raw.startsWith('@@')) {
      seenHunk = true
      const match = raw.match(/@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/)
      if (match) {
        oldLine = Number(match[1])
        newLine = Number(match[2])
      }
      continue
    }
    // ---/+++ are file headers only before the first hunk. Inside a hunk they
    // are content lines whose text starts with "-- " or "++ " (a removed SQL
    // comment, an added diff marker) — eating them would also desync the line
    // counters for the rest of the hunk.
    if (!seenHunk && (raw.startsWith('--- ') || raw.startsWith('+++ '))) continue
    if (raw.startsWith('\\')) continue
    if (raw.startsWith('-')) {
      rows.push({ kind: 'remove', lineNumber: oldLine, text: raw.slice(1) })
      oldLine += 1
      continue
    }
    if (raw.startsWith('+')) {
      rows.push({ kind: 'add', lineNumber: newLine, text: raw.slice(1) })
      newLine += 1
      continue
    }
    // Unified diff context lines carry a leading space; tolerate its absence.
    rows.push({ kind: 'context', lineNumber: newLine, text: raw.startsWith(' ') ? raw.slice(1) : raw })
    oldLine += 1
    newLine += 1
  }
  return rows
}

// computeInlineRanges pairs each removal run with the addition run that
// follows it, index by index, and returns the changed character range of each
// paired line (common prefix/suffix trimmed). Lines left without a pair (one
// side longer, or a pure insertion/deletion) get their whole content marked.
// The result maps the row index in the joined code block to [start, end).
function computeInlineRanges(rows: ParsedDiffLine[]): Map<number, [number, number]> {
  const ranges = new Map<number, [number, number]>()
  let i = 0
  while (i < rows.length) {
    if (rows[i]?.kind === 'context') {
      i += 1
      continue
    }
    const removeStart = i
    while (i < rows.length && rows[i]?.kind === 'remove') i += 1
    const removeEnd = i
    const addStart = i
    while (i < rows.length && rows[i]?.kind === 'add') i += 1
    const addEnd = i

    const paired = Math.min(removeEnd - removeStart, addEnd - addStart)
    for (let k = 0; k < paired; k += 1) {
      const ri = removeStart + k
      const ai = addStart + k
      const rt = rows[ri]?.text ?? ''
      const at = rows[ai]?.text ?? ''
      let prefix = 0
      const maxPrefix = Math.min(rt.length, at.length)
      while (prefix < maxPrefix && rt[prefix] === at[prefix]) prefix += 1
      let suffix = 0
      while (
        suffix < maxPrefix - prefix
        && rt[rt.length - 1 - suffix] === at[at.length - 1 - suffix]
      ) suffix += 1
      if (rt.length - suffix > prefix) ranges.set(ri, [prefix, rt.length - suffix])
      if (at.length - suffix > prefix) ranges.set(ai, [prefix, at.length - suffix])
    }
    for (let k = removeStart + paired; k < removeEnd; k += 1) {
      const text = rows[k]?.text ?? ''
      if (text.length > 0) ranges.set(k, [0, text.length])
    }
    for (let k = addStart + paired; k < addEnd; k += 1) {
      const text = rows[k]?.text ?? ''
      if (text.length > 0) ranges.set(k, [0, text.length])
    }
  }
  return ranges
}

// splitShikiLineHtml extracts the per-line inner HTML from shiki's
// codeToHtml output, whose lines are <span class="line"> elements joined by
// newlines inside one <code> block. Code text is HTML-escaped by shiki, so
// neither the separator nor the closing tag can appear inside a line.
function splitShikiLineHtml(highlighted: string): string[] {
  const lineSpanOpen = '<span class="line">'
  const codeMatch = highlighted.match(/<code>([\s\S]*?)<\/code>/)
  const codeInner = codeMatch?.[1]
  if (!codeInner || !codeInner.startsWith(lineSpanOpen)) {
    throw new Error('unexpected shiki output structure')
  }
  const inner = codeInner.slice(lineSpanOpen.length)
  return inner.split('\n' + lineSpanOpen).map((chunk) =>
    chunk.endsWith('</span>') ? chunk.slice(0, -'</span>'.length) : chunk,
  )
}

const transparentPreTransformer = {
  pre(node: { properties?: Record<string, unknown> }) {
    if (node.properties) {
      delete node.properties.class
      delete node.properties.className
      delete node.properties.style
    }
  },
}

function escapeHtml(str: string): string {
  return str
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

export function resolveLanguage(filename: string): string {
  return getLanguageByFilename(filename)
}

export function extractFilename(path: string): string {
  return path.split('/').pop() ?? path
}
