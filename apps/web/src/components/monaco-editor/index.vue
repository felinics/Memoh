<script setup lang="ts">
import { computed, ref, onMounted, onBeforeUnmount, watch } from 'vue'
import type * as Monaco from 'monaco-editor'
import { useMonaco } from 'stream-monaco'
import { bundledThemes } from 'shiki/themes'
import { useSettingsStore } from '@/store/settings'
// The editor host loads only the minimal `editor.api` entry, which omits every
// optional editor contribution — including sticky scroll. Without this the
// `stickyScroll` option is a silent no-op: nothing pins the current heading at
// the top while scrolling. Pull the contribution in as a side effect so it
// registers against the shared editor instance before any editor is created.
import 'monaco-editor/esm/vs/editor/contrib/stickyScroll/browser/stickyScrollContribution.js'
import { getLanguageByFilename } from '@/components/file-manager/utils'

const props = withDefaults(defineProps<{
  modelValue: string
  language?: string
  readonly?: boolean
  filename?: string
}>(), {
  language: undefined,
  readonly: false,
  filename: undefined,
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const editorRef = ref<HTMLDivElement>()
const settings = useSettingsStore()
const editorFontSize = computed(() => settings.codeFontSizePx)
// Keep Monaco's built-in platform default font unless the user explicitly
// customizes the code font; `undefined` resets to the editor default.
const editorFontFamily = computed(() => settings.codeFontFamily ? settings.codeFontStack : undefined)
let heightObserver: MutationObserver | null = null
let themeObserver: MutationObserver | null = null

function resolveLanguage(): string {
  if (props.language) return props.language
  if (props.filename) return getLanguageByFilename(props.filename)
  return 'plaintext'
}

// ── Markdown outline ──────────────────────────────────────────────────────────
// Monaco ships symbol providers for code (TS/JS/JSON/CSS/HTML) but not Markdown,
// so its sticky-scroll header has nothing to pin on docs. We add a heading parser
// that feeds the same DocumentSymbol pipeline: each heading becomes a section whose
// range spans down to the next heading of equal-or-higher level, nested by depth, so
// sticky-scroll keeps the current heading trail pinned at the top while scrolling.
// Registered once globally (returned disposables intentionally dropped) since the
// provider is keyed by language, not by editor instance.
let markdownSymbolsRegistered = false
function registerMarkdownHeadingSymbols(monaco: typeof Monaco): void {
  if (markdownSymbolsRegistered) return
  markdownSymbolsRegistered = true
  monaco.languages.registerDocumentSymbolProvider('markdown', {
    displayName: 'Markdown headings',
    provideDocumentSymbols: model => buildMarkdownHeadingSymbols(model, monaco),
  })
}

function buildMarkdownHeadingSymbols(model: Monaco.editor.ITextModel, monaco: typeof Monaco): Monaco.languages.DocumentSymbol[] {
  const lineCount = model.getLineCount()
  const headings: { level: number; name: string; line: number }[] = []
  let fenceChar = ''
  for (let i = 1; i <= lineCount; i++) {
    const text = model.getLineContent(i)
    const fence = /^\s*(`{3,}|~{3,})/.exec(text)
    if (fence) {
      const ch = (fence[1] ?? '')[0] ?? ''
      if (!fenceChar) fenceChar = ch
      else if (ch === fenceChar) fenceChar = ''
      continue
    }
    if (fenceChar) continue
    const m = /^(#{1,6})\s+(.*\S)\s*$/.exec(text)
    if (m) headings.push({ level: (m[1] ?? '').length, name: (m[2] ?? '').replace(/\s+#+\s*$/, ''), line: i })
  }

  const symbols: Monaco.languages.DocumentSymbol[] = headings.map((h, idx) => {
    let endLine = lineCount
    for (let j = idx + 1; j < headings.length; j++) {
      const next = headings[j]
      if (next && next.level <= h.level) {
        endLine = Math.max(h.line, next.line - 1)
        break
      }
    }
    return {
      name: h.name || '#',
      detail: '',
      kind: monaco.languages.SymbolKind.String,
      tags: [],
      range: { startLineNumber: h.line, startColumn: 1, endLineNumber: endLine, endColumn: model.getLineMaxColumn(endLine) },
      selectionRange: { startLineNumber: h.line, startColumn: 1, endLineNumber: h.line, endColumn: model.getLineMaxColumn(h.line) },
      children: [],
    }
  })

  const roots: Monaco.languages.DocumentSymbol[] = []
  const stack: { level: number; sym: Monaco.languages.DocumentSymbol }[] = []
  symbols.forEach((sym, idx) => {
    const level = headings[idx]?.level ?? 1
    while (stack.length && (stack[stack.length - 1]?.level ?? 0) >= level) stack.pop()
    const parent = stack[stack.length - 1]
    if (parent?.sym.children) parent.sym.children.push(sym)
    else roots.push(sym)
    stack.push({ level, sym })
  })
  return roots
}

// The applied light/dark mode is driven by the settings store's resolved color
// mode so it stays correct in "system" mode and matches the rest of the UI.
function resolveThemeName(): string {
  return settings.resolvedColorMode === 'dark'
    ? settings.shikiThemeDark
    : settings.shikiThemeLight
}

const {
  createEditor,
  cleanupEditor,
  updateCode,
  setTheme,
  setLanguage,
  getEditor,
  getEditorView,
} = useMonaco({
  theme: resolveThemeName(),
  themes: [settings.shikiThemeDark, settings.shikiThemeLight],
  readOnly: props.readonly,
  automaticLayout: true,
  autoScrollInitial: false,
  autoScrollOnUpdate: false,
  minimap: { enabled: true },
  // Pin the current heading / scope trail at the top while scrolling. Uses the
  // document-symbol model, so it works for code out of the box and for Markdown via
  // the heading provider registered in onBeforeCreate.
  stickyScroll: { enabled: true },
  onBeforeCreate: (monaco) => {
    registerMarkdownHeadingSymbols(monaco as unknown as typeof Monaco)
    return []
  },
  scrollBeyondLastLine: true,
  scrollbar: {
    vertical: 'auto',
    horizontal: 'auto',
    verticalScrollbarSize: 12,
    horizontalScrollbarSize: 12,
    useShadows: true,
  },
  fontSize: editorFontSize.value,
  fontFamily: editorFontFamily.value,
  // Monaco's computed default line box (~1.35×font) packs lines tight, and CJK
  // glyphs (no descender, full em) read bottom-heavy inside it. A roomier
  // explicit lineHeight (1.7×, matching our relaxed prose rhythm) spreads the
  // air evenly above/below and makes consecutive lines distinguishable.
  lineHeight: Math.round(editorFontSize.value * 1.7),
  // Mono at 400 reads thin next to the ~420 Latin body; one step up keeps code
  // legible without approaching bold-token territory (700).
  fontWeight: '500',
  lineNumbers: 'on',
  renderLineHighlight: 'line',
  tabSize: 2,
  wordWrap: 'on',
  padding: { top: 8, bottom: 8 },
})

// ── Minimap fill alignment ────────────────────────────────────────────────────
// The minimap is a painted <canvas>, so it ignores the CSS that pins the rest of
// the editor to the recessed --surface-editor plane and instead fills with the
// theme's own editor background (vitesse pure white / near-black). On a short file
// that left a bright strip down the right edge against the off-white surface.
// The fill can only be changed from inside the Monaco theme, so we re-register the
// active theme with one extra color — `minimap.background` set to the live surface
// token — while leaving every syntax rule untouched. The surface token is resolved
// from the DOM (it shifts with both dark mode and the active color scheme) and the
// theme is rebuilt whenever either changes.

// Minimal textmate→monaco mapping (the same shape stream-monaco produces) so we can
// rebuild the theme with its syntax rules intact and only add the minimap color.
function toHex(color?: string | string[]): string | undefined {
  if (Array.isArray(color)) color = color[0]
  if (!color) return undefined
  color = (color.charCodeAt(0) === 35 ? color.slice(1) : color).toLowerCase()
  if (color.length === 3 || color.length === 4) color = color.split('').map(c => c + c).join('')
  return color
}

const FONT_STYLES = ['italic', 'bold', 'underline', 'strikethrough'] as const
function toFontStyle(fontStyle?: string): string {
  if (!fontStyle) return ''
  const set = new Set(fontStyle.split(/[\s,]+/).map(s => s.trim().toLowerCase()))
  return FONT_STYLES.filter(s => set.has(s)).join(' ')
}

interface ShikiThemeLike {
  type?: string
  colors?: Record<string, string>
  tokenColors?: { scope?: string | string[]; settings?: { foreground?: string; background?: string; fontStyle?: string } }[]
}

function toMonacoTheme(theme: ShikiThemeLike) {
  const rules: { token: string; foreground?: string; background?: string; fontStyle?: string }[] = []
  for (const entry of theme.tokenColors ?? []) {
    const { foreground, background, fontStyle } = entry.settings ?? {}
    if (!foreground && !background && !fontStyle) continue
    const scopes = Array.isArray(entry.scope) ? entry.scope : entry.scope ? [entry.scope] : []
    const fg = toHex(foreground)
    const bg = toHex(background)
    const fs = toFontStyle(fontStyle)
    for (const s of scopes) rules.push({ token: s, foreground: fg, background: bg, fontStyle: fs })
  }
  const colors: Record<string, string> = {}
  for (const [k, v] of Object.entries(theme.colors ?? {})) {
    const n = toHex(v)
    if (n) colors[k] = `#${n}`
  }
  return { base: theme.type === 'light' ? 'vs' : 'vs-dark', inherit: false, colors, rules } as const
}

const shikiThemeCache = new Map<string, ShikiThemeLike>()
async function loadShikiTheme(name: string): Promise<ShikiThemeLike | null> {
  const cached = shikiThemeCache.get(name)
  if (cached) return cached
  const loader = (bundledThemes as Record<string, (() => Promise<{ default: ShikiThemeLike }>) | undefined>)[name]
  if (!loader) return null
  const theme = (await loader()).default
  shikiThemeCache.set(name, theme)
  return theme
}

// Resolve --surface-editor to a concrete sRGB hex. The token is authored in oklch and
// varies per scheme, so we read the live computed value off a throwaway probe and round-
// trip it through a 1px canvas, which yields the actual painted bytes regardless of the
// CSS color space.
function resolveSurfaceHex(): string | null {
  if (typeof document === 'undefined') return null
  const probe = document.createElement('div')
  probe.style.cssText = 'position:fixed;left:-9999px;top:-9999px;width:1px;height:1px;background-color:var(--surface-editor)'
  ;(document.body ?? document.documentElement).appendChild(probe)
  const computed = getComputedStyle(probe).backgroundColor
  probe.remove()
  if (!computed) return null
  const canvas = document.createElement('canvas')
  canvas.width = 1
  canvas.height = 1
  const ctx = canvas.getContext('2d')
  if (!ctx) return null
  ctx.fillStyle = '#000000'
  ctx.fillStyle = computed
  ctx.fillRect(0, 0, 1, 1)
  try {
    const data = ctx.getImageData(0, 0, 1, 1).data
    const channels = [data[0] ?? 0, data[1] ?? 0, data[2] ?? 0]
    return `#${channels.map(x => x.toString(16).padStart(2, '0')).join('')}`
  } catch {
    return null
  }
}

let syncToken = 0
async function syncEditorTheme(): Promise<void> {
  if (!getEditorView()) return
  const name = resolveThemeName()
  const surface = resolveSurfaceHex()
  const token = ++syncToken
  const shikiTheme = await loadShikiTheme(name)
  if (token !== syncToken || !getEditorView()) return
  if (shikiTheme && surface) {
    const monacoTheme = toMonacoTheme(shikiTheme)
    monacoTheme.colors['minimap.background'] = surface
    getEditor().defineTheme(name, monacoTheme as Parameters<ReturnType<typeof getEditor>['defineTheme']>[1])
  }
  await setTheme(name, true)
}

function clearInlineHeightStyles(el: HTMLElement) {
  let changed = false
  for (const prop of ['height', 'max-height', 'min-height', 'overflow'] as const) {
    if (el.style.getPropertyValue(prop)) {
      el.style.removeProperty(prop)
      changed = true
    }
  }
  return changed
}

onMounted(async () => {
  if (!editorRef.value) return

  await createEditor(editorRef.value, props.modelValue, resolveLanguage())

  clearInlineHeightStyles(editorRef.value)

  heightObserver = new MutationObserver(() => {
    if (editorRef.value) clearInlineHeightStyles(editorRef.value)
  })
  heightObserver.observe(editorRef.value, { attributes: true, attributeFilter: ['style'] })

  void syncEditorTheme()

  // Mode (`class`) and color scheme (`data-color-scheme`) both live on <html> and
  // both move the surface token, so rebuild the minimap fill whenever either flips.
  themeObserver = new MutationObserver(() => {
    void syncEditorTheme()
  })
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-color-scheme'] })

  const editor = getEditorView()
  if (editor) {
    editor.updateOptions({
      fontSize: editorFontSize.value,
      fontFamily: editorFontFamily.value,
    })
    editor.setPosition({ lineNumber: 1, column: 1 })
    editor.revealLine(1)
  }
  editor?.onDidChangeModelContent(() => {
    const value = editor.getValue() ?? ''
    emit('update:modelValue', value)
  })
})

onBeforeUnmount(() => {
  heightObserver?.disconnect()
  heightObserver = null
  themeObserver?.disconnect()
  themeObserver = null
  cleanupEditor()
})

watch(() => props.modelValue, (newVal) => {
  const editor = getEditorView()
  if (!editor) return
  if (editor.getValue() !== newVal) {
    updateCode(newVal, resolveLanguage())
  }
})

watch(() => props.readonly, (val) => {
  getEditorView()?.updateOptions({ readOnly: val })
})

watch(editorFontSize, (fontSize) => {
  getEditorView()?.updateOptions({ fontSize, lineHeight: Math.round(fontSize * 1.7) })
})

watch(editorFontFamily, (fontFamily) => {
  getEditorView()?.updateOptions({ fontFamily })
})

watch([() => props.language, () => props.filename], () => {
  setLanguage(resolveLanguage())
})

watch(
  () => [settings.shikiThemeLight, settings.shikiThemeDark, settings.resolvedColorMode] as const,
  () => { void syncEditorTheme() },
)
</script>

<template>
  <div
    ref="editorRef"
    class="h-full w-full overflow-hidden bg-surface-editor"
  />
</template>

<style scoped>
/* Align Monaco's editor + gutter fill with the workspace editor plane so the code
 * surface is continuous with its tab (no white-on-canvas seam). vitesse's syntax
 * token colors are untouched — only the background fill is nudged to the surface
 * token. !important beats Monaco's non-important inline fill. The minimap is a
 * canvas and can't be reached here; its fill is matched via the theme instead
 * (see syncEditorTheme). */
:deep(.monaco-editor),
:deep(.monaco-editor .overflow-guard),
:deep(.monaco-editor-background),
:deep(.monaco-editor .margin) {
  background-color: var(--surface-editor) !important;
}

/* Sticky scroll header. The theme paints it with vitesse's raw editor background,
 * which sits a shade off the recessed --surface-editor plane the breadcrumb and
 * code surface share — so the pinned band read as a brighter slab. Pin it to the
 * same surface token instead. Drop Monaco's drop-shadow for a single hairline
 * divider (flatter, consistent with the rest of the chrome). Keep the widget below
 * the minimap (its native z 4 < minimap z 5) so the minimap stays on top: the
 * divider runs from the left edge and is occluded at the minimap's left edge,
 * rather than painting over the minimap. */
:deep(.monaco-editor .sticky-widget) {
  background-color: var(--surface-editor) !important;
  box-shadow: none !important;
  border-bottom: 1px solid var(--border) !important;
}
</style>
