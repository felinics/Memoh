import type * as Monaco from 'monaco-editor'
import { bundledThemes } from 'shiki/themes'

interface ShikiThemeLike {
  type?: string
  colors?: Record<string, string>
  tokenColors?: {
    scope?: string | string[]
    settings?: { foreground?: string; background?: string; fontStyle?: string }
  }[]
}

interface BaseMonacoTheme {
  base: 'vs' | 'vs-dark'
  rules: Monaco.editor.ITokenThemeRule[]
  colors: Record<string, string>
}

const FONT_STYLES = ['italic', 'bold', 'underline', 'strikethrough'] as const

function toHex(color?: string | string[]): string | undefined {
  if (Array.isArray(color)) color = color[0]
  if (!color) return undefined
  color = (color.charCodeAt(0) === 35 ? color.slice(1) : color).toLowerCase()
  if (color.length === 3 || color.length === 4) color = color.split('').map(c => c + c).join('')
  return color
}

function toFontStyle(fontStyle?: string): string {
  if (!fontStyle) return ''
  const set = new Set(fontStyle.split(/[\s,]+/).map(s => s.trim().toLowerCase()))
  return FONT_STYLES.filter(s => set.has(s)).join(' ')
}

function convertShikiTheme(theme: ShikiThemeLike): BaseMonacoTheme {
  const rules: Monaco.editor.ITokenThemeRule[] = []
  for (const entry of theme.tokenColors ?? []) {
    const { foreground, background, fontStyle } = entry.settings ?? {}
    if (!foreground && !background && !fontStyle) continue
    const scopes = Array.isArray(entry.scope) ? entry.scope : entry.scope ? [entry.scope] : []
    const fg = toHex(foreground)
    const bg = toHex(background)
    const fs = toFontStyle(fontStyle)
    for (const scope of scopes) {
      rules.push({ token: scope, foreground: fg, background: bg, fontStyle: fs })
    }
  }
  const colors: Record<string, string> = {}
  for (const [key, val] of Object.entries(theme.colors ?? {})) {
    const hex = toHex(val)
    if (hex) colors[key] = `#${hex}`
  }
  return {
    base: theme.type === 'light' ? 'vs' : 'vs-dark',
    rules,
    colors,
  }
}

const baseThemeCache = new Map<string, Promise<BaseMonacoTheme | null>>()
const surfaceHexCache = new Map<string, string>()
let sharedCanvasCtx: CanvasRenderingContext2D | null | undefined

function loadBaseMonacoTheme(name: string): Promise<BaseMonacoTheme | null> {
  const existing = baseThemeCache.get(name)
  if (existing) return existing
  const loader = (bundledThemes as Record<string, (() => Promise<{ default: ShikiThemeLike }>) | undefined>)[name]
  const pending = loader
    ? loader().then(mod => convertShikiTheme(mod.default)).catch(() => null)
    : Promise.resolve(null)
  baseThemeCache.set(name, pending)
  return pending
}

function getSharedCanvasCtx(): CanvasRenderingContext2D | null {
  if (sharedCanvasCtx !== undefined) return sharedCanvasCtx
  if (typeof document === 'undefined') return null
  const canvas = document.createElement('canvas')
  canvas.width = 1
  canvas.height = 1
  sharedCanvasCtx = canvas.getContext('2d', { willReadFrequently: true })
  return sharedCanvasCtx
}

// Resolve `--surface-editor` (authored in oklch per scheme) to #rrggbb once per
// `(colorMode, colorScheme)` pair after root attributes have settled.
function resolveEditorSurfaceHex(colorMode: string, colorScheme: string): string | null {
  if (typeof document === 'undefined') return null
  const root = document.documentElement
  const domMatchesState = root.classList.contains('dark') === (colorMode === 'dark')
    && (root.dataset.colorScheme || 'memoh') === colorScheme
  const cacheKey = `${colorMode}:${colorScheme}`
  if (domMatchesState) {
    const cached = surfaceHexCache.get(cacheKey)
    if (cached) return cached
  }

  const probe = document.createElement('div')
  probe.style.cssText = 'position:fixed;left:-9999px;top:-9999px;width:1px;height:1px;background-color:var(--surface-editor)'
  ;(document.body ?? root).appendChild(probe)
  const computed = getComputedStyle(probe).backgroundColor
  probe.remove()
  if (!computed) return null

  const ctx = getSharedCanvasCtx()
  if (!ctx) return null
  ctx.fillStyle = '#000000'
  ctx.fillStyle = computed
  ctx.fillRect(0, 0, 1, 1)
  try {
    const data = ctx.getImageData(0, 0, 1, 1).data
    const hex = `#${[data[0] ?? 0, data[1] ?? 0, data[2] ?? 0].map(x => x.toString(16).padStart(2, '0')).join('')}`
    if (domMatchesState) surfaceHexCache.set(cacheKey, hex)
    return hex
  } catch {
    return null
  }
}

// Re-registers `themeName` with `minimap.background` matched to `--surface-editor`.
// Called from `useMonaco({ onThemeChange })` so incremental grammar loads via
// `@shikijs/monaco` (which overwrite `defineTheme` with raw Shiki colors) and
// A->B->A theme switches always keep the minimap canvas aligned with the editor plane.
export async function applyMonacoMinimapSurface(
  editorApi: Pick<typeof Monaco.editor, 'defineTheme' | 'setTheme'>,
  themeName: string,
  colorMode: string,
  colorScheme: string,
): Promise<void> {
  const surface = resolveEditorSurfaceHex(colorMode, colorScheme)
  const base = await loadBaseMonacoTheme(themeName)
  if (!base || !surface) return
  editorApi.defineTheme(themeName, {
    base: base.base,
    inherit: false,
    rules: base.rules,
    colors: {
      ...base.colors,
      'minimap.background': surface,
    },
  })
  editorApi.setTheme(themeName)
}
