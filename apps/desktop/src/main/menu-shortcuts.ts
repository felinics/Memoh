import type { KeyboardPlatform } from '../../../web/src/lib/keyboard-bindings'
import { matchesMenuAccelerator } from '../shared/keyboard-commands'

export interface MenuShortcutInput {
  key: string
  code?: string
  control: boolean
  meta: boolean
  alt: boolean
  shift: boolean
  isAutoRepeat?: boolean
  isComposing: boolean
}

interface MenuShortcutContents {
  on(event: string, listener: (...args: never[]) => void): unknown
  setIgnoreMenuShortcuts(ignore: boolean): void
}

export function shouldIgnoreMenuShortcuts(
  input: MenuShortcutInput,
  capture: boolean,
  closeAccelerator: string | undefined,
  platform: KeyboardPlatform,
): boolean {
  const key = { key: input.key, code: input.code, ctrlKey: input.control, metaKey: input.meta, altKey: input.alt, shiftKey: input.shift }
  return capture || input.isComposing || matchesMenuAccelerator(key, closeAccelerator, platform)
}

export function attachMenuShortcutOwnership(
  contents: MenuShortcutContents,
  closeAccelerator: () => string | undefined,
  platform: KeyboardPlatform,
) {
  let capture = false
  contents.on('before-input-event', (_event: unknown, input: MenuShortcutInput) => {
    contents.setIgnoreMenuShortcuts(shouldIgnoreMenuShortcuts(input, capture, closeAccelerator(), platform))
  })
  contents.on('did-start-navigation', (event: { isMainFrame: boolean, isSameDocument: boolean }) => {
    if (!event.isMainFrame || event.isSameDocument) return
    capture = false
    contents.setIgnoreMenuShortcuts(false)
  })
  return {
    setCapture(sender: unknown, ignore: unknown) {
      if (sender !== contents || typeof ignore !== 'boolean') return
      capture = ignore
      contents.setIgnoreMenuShortcuts(ignore)
    },
  }
}
