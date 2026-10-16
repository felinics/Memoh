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
  readonly focusedFrame?: { parent: unknown } | null
}

/**
 * `page` hands the key to the DOM listener, `menu` lets the native accelerator
 * fire, `drop` swallows a held key. Close belongs to the page, except while a
 * subframe has focus: its keys never reach the host DOM, so the menu keeps it.
 */
function menuShortcutOwner(
  input: MenuShortcutInput,
  capture: boolean,
  closeAccelerator: string | undefined,
  platform: KeyboardPlatform,
  subframeFocused: boolean,
): 'page' | 'menu' | 'drop' {
  if (capture || input.isComposing) return 'page'
  const key = { key: input.key, code: input.code, ctrlKey: input.control, metaKey: input.meta, altKey: input.alt, shiftKey: input.shift }
  if (!matchesMenuAccelerator(key, closeAccelerator, platform)) return 'menu'
  if (!subframeFocused) return 'page'
  return input.isAutoRepeat ? 'drop' : 'menu'
}

export function attachMenuShortcutOwnership(
  contents: MenuShortcutContents,
  closeAccelerator: () => string | undefined,
  platform: KeyboardPlatform,
) {
  let capture = false
  contents.on('before-input-event', (event: { preventDefault(): void }, input: MenuShortcutInput) => {
    const owner = menuShortcutOwner(input, capture, closeAccelerator(), platform, contents.focusedFrame?.parent != null)
    if (owner === 'drop') event.preventDefault()
    contents.setIgnoreMenuShortcuts(owner !== 'menu')
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
