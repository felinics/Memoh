import { computed, inject, onScopeDispose, ref, type ComputedRef } from 'vue'
import { DesktopShellKey, DesktopWindowKey } from '@/lib/desktop-shell'

// Whether the layout must reserve the macOS traffic-light strip/gutter.
// True only inside the Electron desktop shell on macOS — and only while
// windowed: macOS hides the traffic lights in fullscreen, so keeping the
// reserve there would leave dead space. Without the desktop window bridge
// (browser, or the ?desktopShell=1 recording hatch) fullscreen never
// happens, so the reserve stays constant.
export function useMacTrafficReserve(): ComputedRef<boolean> {
  const desktopShell = inject(DesktopShellKey, false)
  const windowBridge = inject(DesktopWindowKey, undefined)

  const fullScreen = ref(false)
  if (desktopShell && windowBridge) {
    void windowBridge.isFullScreen()
      .then((value) => { fullScreen.value = value })
      .catch(() => {})
    onScopeDispose(windowBridge.onFullScreenChanged((value) => { fullScreen.value = value }))
  }

  return computed(() =>
    desktopShell
    && !fullScreen.value
    && typeof navigator !== 'undefined'
    && navigator.platform.toLowerCase().includes('mac'),
  )
}
