import { onBeforeUnmount, onMounted } from 'vue'
import type { Router } from 'vue-router'
import { canGoBack } from '@/composables/useBackOr'

// The contract between this web app and the native mobile shell (apps/mobile).
// Everything the shell asks of the page goes through this module, so the page
// side of the contract lives in one place.
//
// System back follows Android's navigation principles: what the user sees is
// one back stack, back pops its top and nothing else, the chat is the fixed
// start destination, and back at the start leaves the app. An on-screen back
// button and system back do the same thing.
//
// On a back gesture the shell calls window.__handleNativeBack(), which asks,
// in this order, the first one that applies wins:
//   1. An open overlay (dialog, sheet, menu, popover) closes, as on Escape.
//   2. Views that are not routes (the mobile navigation page and its files /
//      schedules pages) and sections whose back has a parent (settings)
//      register a handler while mounted; the latest registered is asked
//      first, so a view opened on top of another gets the back before it.
//   3. Off the start destination, a page with an earlier entry of this app in
//      history pops it.
// Nothing applies → 'exit', and the shell leaves the app. The shell never
// walks browser history itself: entries it cannot see the meaning of are
// exactly how back used to land somewhere the user had not just been.

export type NativeBackResult = 'handled' | 'exit'
// Returns true when the handler consumed the back.
export type NativeBackHandler = () => boolean

type NativeBackWindow = Window & { __handleNativeBack?: () => NativeBackResult }

const backHandlers: NativeBackHandler[] = []
let appRouter: Router | null = null

// Every overlay of the component library dismisses on Escape (Reka's
// DismissableLayer), so back closes the topmost one the same way.
const OPEN_OVERLAY = '[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"], [role="menu"][data-state="open"], [data-reka-popper-content-wrapper]'

// Also read by views that close on Escape themselves, so one Escape (or back)
// closes only the overlay on top of them.
export function hasOpenOverlay(): boolean {
  return document.querySelector(OPEN_OVERLAY) !== null
}

function closeTopOverlay(): boolean {
  if (!hasOpenOverlay()) return false
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  return true
}

const START_ROUTES = new Set(['home', 'bot'])

function popPage(): boolean {
  if (!appRouter) return false
  if (START_ROUTES.has(String(appRouter.currentRoute.value.name))) return false
  if (!canGoBack(appRouter)) return false
  appRouter.back()
  return true
}

function handleNativeBack(): NativeBackResult {
  if (closeTopOverlay()) return 'handled'
  for (let i = backHandlers.length - 1; i >= 0; i--) {
    if (backHandlers[i]!()) return 'handled'
  }
  return popPage() ? 'handled' : 'exit'
}

export function onNativeBack(handler: NativeBackHandler): () => void {
  backHandlers.push(handler)
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
export function installNativeShell(router: Router): void {
  appRouter = router
  ;(window as NativeBackWindow).__handleNativeBack = handleNativeBack
  const nativeWindow = window as NativeShellWindow
  if (!nativeWindow.MemohNative) return
  const lastKey = { value: '' }
  const sync = () => syncSystemBars(nativeWindow, lastKey)
  new MutationObserver(sync).observe(document.documentElement, { attributes: true })
  sync()
}
