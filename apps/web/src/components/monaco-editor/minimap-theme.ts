import type * as Monaco from 'monaco-editor'

// The minimap is painted on a canvas from the theme's `minimap.background`, so
// CSS cannot align it with the `--surface-editor` plane the rest of the editor
// sits on. Themes are registered by several parties and re-registered with the
// raw Shiki colors every time a grammar loads (`@shikijs/monaco`), so the
// surface is merged in at the single entry they all go through:
// `monaco.editor.defineTheme`.

type MonacoApi = { editor: Pick<typeof Monaco.editor, 'defineTheme'> }
type DefineTheme = typeof Monaco.editor.defineTheme

const surfaceHexCache = new Map<string, string>()
let sharedCanvasCtx: CanvasRenderingContext2D | null | undefined

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

// Theme data as registered by its owner, without the surface override, so the
// themes can be registered again when the surface color changes.
const registeredThemes = new Map<string, Monaco.editor.IStandaloneThemeData>()
let defineThemeOnMonaco: DefineTheme | null = null
let currentSurface: string | null = null

function withMinimapSurface(theme: Monaco.editor.IStandaloneThemeData): Monaco.editor.IStandaloneThemeData {
  if (!currentSurface) return theme
  return { ...theme, colors: { ...theme.colors, 'minimap.background': currentSurface } }
}

// Call from `useMonaco({ onBeforeCreate })`, which runs before the editor's
// themes are registered. Wraps the shared Monaco instance once; later calls
// only bring the surface up to date.
export function installMinimapSurface(monaco: MonacoApi, colorMode: string, colorScheme: string): void {
  if (!defineThemeOnMonaco) {
    const original = monaco.editor.defineTheme.bind(monaco.editor)
    defineThemeOnMonaco = original
    monaco.editor.defineTheme = (name, theme) => {
      registeredThemes.set(name, theme)
      original(name, withMinimapSurface(theme))
    }
  }
  syncMinimapSurface(colorMode, colorScheme)
}

// Call after the root color mode / scheme attributes have settled and before
// switching the editor theme: every theme is registered again with the new
// surface, so the `setTheme` that follows applies a theme that already has it.
export function syncMinimapSurface(colorMode: string, colorScheme: string): void {
  if (!defineThemeOnMonaco) return
  const surface = resolveEditorSurfaceHex(colorMode, colorScheme)
  if (!surface || surface === currentSurface) return
  currentSurface = surface
  for (const [name, theme] of registeredThemes) {
    defineThemeOnMonaco(name, withMinimapSurface(theme))
  }
}
