<script setup lang="ts">
import { computed, ref, onMounted, onBeforeUnmount, watch } from 'vue'
import type * as Monaco from 'monaco-editor'
import { useMonaco } from 'stream-monaco'
import { useSettingsStore } from '@/store/settings'
// The editor host loads only the minimal `editor.api` entry, which omits every
// optional editor contribution — including sticky scroll. Without this the
// `stickyScroll` option is a silent no-op: nothing pins the current heading at
// the top while scrolling. Pull the contribution in as a side effect so it
// registers against the shared editor instance before any editor is created.
import 'monaco-editor/esm/vs/editor/contrib/stickyScroll/browser/stickyScrollContribution.js'
import { getLanguageByFilename } from '@/components/file-manager/utils'
import { applyMonacoMinimapSurface } from './minimap-theme'

const props = withDefaults(defineProps<{
  modelValue: string
  language?: string
  readonly?: boolean
  filename?: string
  active?: boolean
  initialViewState?: Monaco.editor.ICodeEditorViewState | null
}>(), {
  language: undefined,
  readonly: false,
  filename: undefined,
  active: true,
  initialViewState: null,
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'update:viewState': [state: Monaco.editor.ICodeEditorViewState | null]
}>()

const editorRef = ref<HTMLDivElement>()
const settings = useSettingsStore()
const editorFontSize = computed(() => settings.codeFontSizePx)
// Keep Monaco's built-in platform default font unless the user explicitly
// customizes the code font; `undefined` resets to the editor default.
const editorFontFamily = computed(() => settings.codeFontFamily ? settings.codeFontStack : undefined)
let heightObserver: MutationObserver | null = null
let viewStateDisposables: Monaco.IDisposable[] = []
let themeFrame = 0
let themeDirtyWhileInactive = false
let mounted = false

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
  onThemeChange: async (themeName) => {
    await applyMonacoMinimapSurface(
      getEditor() as unknown as Pick<typeof Monaco.editor, 'defineTheme' | 'setTheme'>,
      String(themeName),
      settings.resolvedColorMode,
      settings.colorScheme,
    )
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

function scheduleThemeSync() {
  if (!props.active) {
    themeDirtyWhileInactive = true
    return
  }
  if (themeFrame) cancelAnimationFrame(themeFrame)
  themeFrame = requestAnimationFrame(() => {
    themeFrame = 0
    if (!getEditorView()) return
    void setTheme(resolveThemeName(), true)
  })
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
  mounted = true
  if (!editorRef.value) return

  await createEditor(editorRef.value, props.modelValue, resolveLanguage())
  if (!mounted || !editorRef.value) return

  clearInlineHeightStyles(editorRef.value)

  heightObserver = new MutationObserver(() => {
    if (editorRef.value) clearInlineHeightStyles(editorRef.value)
  })
  heightObserver.observe(editorRef.value, { attributes: true, attributeFilter: ['style'] })

  const editor = getEditorView()
  if (editor) {
    editor.updateOptions({
      fontSize: editorFontSize.value,
      fontFamily: editorFontFamily.value,
    })
    if (props.initialViewState) {
      editor.restoreViewState(props.initialViewState)
    } else {
      editor.setPosition({ lineNumber: 1, column: 1 })
      editor.revealLine(1)
    }
    const captureLiveViewState = () => {
      if (!props.active) return
      const host = editorRef.value
      if (!host || host.clientWidth <= 0 || host.clientHeight <= 0) return
      emit('update:viewState', editor.saveViewState())
    }
    viewStateDisposables.push(
      editor.onDidChangeCursorSelection(captureLiveViewState),
      editor.onDidScrollChange(captureLiveViewState),
      editor.onDidChangeHiddenAreas(captureLiveViewState),
    )
    editor.onDidChangeModelContent(() => {
      const value = editor.getValue() ?? ''
      emit('update:modelValue', value)
    })
  }
})

onBeforeUnmount(() => {
  mounted = false
  if (themeFrame) {
    cancelAnimationFrame(themeFrame)
    themeFrame = 0
  }
  for (const d of viewStateDisposables) d.dispose()
  viewStateDisposables = []
  heightObserver?.disconnect()
  heightObserver = null
  cleanupEditor()
})

watch(() => props.active, (active) => {
  if (!active) return
  getEditorView()?.layout()
  if (themeDirtyWhileInactive) {
    themeDirtyWhileInactive = false
    scheduleThemeSync()
  }
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
  () => [settings.shikiThemeLight, settings.shikiThemeDark, settings.resolvedColorMode, settings.colorScheme] as const,
  scheduleThemeSync,
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
 * (see applyMonacoMinimapSurface). */
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
