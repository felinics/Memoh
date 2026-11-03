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
