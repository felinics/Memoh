import type { BrowserWindow } from 'electron'
import { is, optimizer } from '@electron-toolkit/utils'

// Electron Toolkit's packaged-app handler blocks the native DevTools
// accelerators. Memoh deliberately keeps standard Electron DevTools available;
// only development needs Toolkit's extra F12 toggle behavior.
export function attachDevelopmentWindowShortcuts(window: BrowserWindow): void {
  if (!is.dev) return
  optimizer.watchWindowShortcuts(window)
}
