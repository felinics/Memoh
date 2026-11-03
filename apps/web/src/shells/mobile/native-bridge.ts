import { onBeforeUnmount, onMounted } from 'vue'

// The contract between this web app and the native mobile shell (apps/mobile).
// Everything the shell asks of the page goes through this module, so the page
// side of the contract lives in one place.
//
// System back. Some mobile views are not routes (the navigation page, the
// files and schedules pages inside it), so browser history cannot step out of
// them. On a system back gesture the shell evaluates
// window.__handleNativeBack() first: 'handled' means the page consumed the
// back; 'exit' means it did not, and the shell falls back to history, then to
// leaving the app. In a browser nothing calls it and history alone applies.
//
// Views register a handler while they are mounted. The most recently
// registered one is asked first, so a view opened on top of another gets the
// back before the one beneath it.

export type NativeBackResult = 'handled' | 'exit'
// Returns true when the handler consumed the back.
export type NativeBackHandler = () => boolean

type NativeBackWindow = Window & { __handleNativeBack?: () => NativeBackResult }

const backHandlers: NativeBackHandler[] = []

function handleNativeBack(): NativeBackResult {
  for (let i = backHandlers.length - 1; i >= 0; i--) {
    if (backHandlers[i]!()) return 'handled'
  }
  return 'exit'
}

export function onNativeBack(handler: NativeBackHandler): () => void {
  backHandlers.push(handler)
  ;(window as NativeBackWindow).__handleNativeBack = handleNativeBack
  return () => {
    const index = backHandlers.lastIndexOf(handler)
    if (index !== -1) backHandlers.splice(index, 1)
  }
}

// Registers the handler for as long as the calling component is mounted.
export function useNativeBack(handler: NativeBackHandler): void {
  let unregister: (() => void) | null = null
  onMounted(() => {
    unregister = onNativeBack(handler)
  })
  onBeforeUnmount(() => {
    unregister?.()
    unregister = null
  })
}

// System bars. The page draws under the status bar and the gesture bar, so
// their icons must follow the page's theme, not the phone's: a dark page on a
// phone in light mode would otherwise get dark icons on a dark background.
// Two calls, in this order, because SystemBars.setStyle resets the window
// background:
//   1. Capacitor's SystemBars plugin sets the icon style. Its styles name the
//      page under the bar: LIGHT means dark icons for a light page.
//   2. MemohNative.setCanvasColor sets the window background, which shows at
//      launch and behind the keyboard while it animates.
// The page loads no Capacitor JS bundle, so plugins are reached through the
// bridge's nativePromise rather than Capacitor.Plugins.
type NativeShellWindow = Window & {
  MemohNative?: { setCanvasColor: (hex: string) => void }
  Capacitor?: {
    nativePromise?: (plugin: string, method: string, options: object) => Promise<unknown>
  }
}

// setCanvasColor takes #rrggbb (Android's Color.parseColor); the theme
// tokens may be any CSS color, oklch included, so let the browser resolve it.
function toHexColor(cssColor: string): string | null {
  const context = document.createElement('canvas').getContext('2d')
  if (!context) return null
  context.fillStyle = cssColor
  context.fillRect(0, 0, 1, 1)
  const [r, g, b] = context.getImageData(0, 0, 1, 1).data
  return `#${[r, g, b].map(channel => (channel ?? 0).toString(16).padStart(2, '0')).join('')}`
}

function syncSystemBars(nativeWindow: NativeShellWindow, lastKey: { value: string }): void {
  const root = document.documentElement
  const dark = root.classList.contains('dark')
  const canvas = toHexColor(getComputedStyle(root).getPropertyValue('--background').trim())
  if (!canvas) return
  const key = `${dark} ${canvas}`
  if (key === lastKey.value) return
  lastKey.value = key
  const sendCanvas = () => {
    try {
      nativeWindow.MemohNative?.setCanvasColor(canvas)
    }
    catch {
      // An older shell without the method keeps its launch color.
    }
  }
  const nativePromise = nativeWindow.Capacitor?.nativePromise
  if (!nativePromise) {
    sendCanvas()
    return
  }
  nativePromise('SystemBars', 'setStyle', { style: dark ? 'DARK' : 'LIGHT' })
    .catch(() => {})
    .then(sendCanvas)
}

// Called once at startup; does nothing outside the native shell. The theme
// lives on <html>: the dark class, plus attributes that swap the palette.
export function installNativeShell(): void {
  const nativeWindow = window as NativeShellWindow
  if (!nativeWindow.MemohNative) return
  const lastKey = { value: '' }
  const sync = () => syncSystemBars(nativeWindow, lastKey)
  new MutationObserver(sync).observe(document.documentElement, { attributes: true })
  sync()
}
