import { describe, expect, it, vi } from 'vitest'
import { attachMenuShortcutOwnership, type MenuShortcutInput } from './menu-shortcuts'

function createContents() {
  const listeners = new Map<string, (...args: never[]) => void>()
  return {
    on: vi.fn((event: string, listener: (...args: never[]) => void) => { listeners.set(event, listener) }),
    setIgnoreMenuShortcuts: vi.fn(),
    input(input: Partial<MenuShortcutInput>) {
      const listener = listeners.get('before-input-event') as (event: unknown, input: MenuShortcutInput) => void
      listener({}, { key: 'r', control: false, meta: false, alt: false, shift: false, isAutoRepeat: false, isComposing: false, ...input })
    },
    navigate(event: { isMainFrame: boolean, isSameDocument: boolean }) {
      (listeners.get('did-start-navigation') as (event: { isMainFrame: boolean, isSameDocument: boolean }) => void)(event)
    },
  }
}

function attach(platform: 'mac' | 'linux' = 'linux') {
  const contents = createContents()
  let accelerator = 'CmdOrCtrl+W'
  const ownership = attachMenuShortcutOwnership(contents, () => accelerator, platform)
  const lastIgnore = () => contents.setIgnoreMenuShortcuts.mock.lastCall?.[0]
  return { contents, ownership, lastIgnore, rebind: (next: string) => { accelerator = next } }
}

describe('native menu shortcut ownership', () => {
  it('keeps held native menu shortcuts repeating', () => {
    const { contents, lastIgnore } = attach()
    contents.input({ key: 'r', control: true })
    expect(lastIgnore()).toBe(false)
    contents.input({ key: 'r', control: true, isAutoRepeat: true })
    expect(lastIgnore()).toBe(false)
    contents.input({ key: 'z', control: true, isAutoRepeat: true })
    expect(lastIgnore()).toBe(false)
  })

  it('hands the effective Close accelerator to the DOM listener, including repeats', () => {
    const { contents, lastIgnore, rebind } = attach()
    contents.input({ key: 'w', control: true })
    expect(lastIgnore()).toBe(true)
    contents.input({ key: 'w', control: true, isAutoRepeat: true })
    expect(lastIgnore()).toBe(true)
    rebind('CmdOrCtrl+Alt+W')
    contents.input({ key: 'w', control: true })
    expect(lastIgnore()).toBe(false)
    contents.input({ key: 'w', control: true, alt: true })
    expect(lastIgnore()).toBe(true)
  })

  it('matches the macOS Command+Option Close accelerator by the physical key', () => {
    const { contents, lastIgnore, rebind } = attach('mac')
    rebind('CmdOrCtrl+Alt+W')
    contents.input({ key: '∑', code: 'KeyW', meta: true, alt: true })
    expect(lastIgnore()).toBe(true)
  })

  it('leaves keys to an active input method', () => {
    const { contents, lastIgnore } = attach()
    contents.input({ key: 'r', control: true, isComposing: true })
    expect(lastIgnore()).toBe(true)
  })

  it('suspends every native menu shortcut while the chat renderer records a shortcut', () => {
    const { contents, ownership, lastIgnore } = attach()
    ownership.setCapture(contents, true)
    expect(lastIgnore()).toBe(true)
    contents.input({ key: 'r', control: true })
    expect(lastIgnore()).toBe(true)
    ownership.setCapture(contents, false)
    contents.input({ key: 'r', control: true })
    expect(lastIgnore()).toBe(false)
  })

  it('accepts capture changes only as booleans from its own renderer', () => {
    const { contents, ownership, lastIgnore } = attach()
    ownership.setCapture({}, true)
    ownership.setCapture(contents, 'true')
    expect(contents.setIgnoreMenuShortcuts).not.toHaveBeenCalled()
    contents.input({ key: 'r', control: true })
    expect(lastIgnore()).toBe(false)
  })

  it('releases capture when the main frame loads another document', () => {
    const { contents, ownership, lastIgnore } = attach()
    ownership.setCapture(contents, true)
    contents.navigate({ isMainFrame: true, isSameDocument: true })
    contents.navigate({ isMainFrame: false, isSameDocument: false })
    contents.input({ key: 'r', control: true })
    expect(lastIgnore()).toBe(true)
    contents.navigate({ isMainFrame: true, isSameDocument: false })
    expect(lastIgnore()).toBe(false)
    contents.input({ key: 'r', control: true })
    expect(lastIgnore()).toBe(false)
  })
})
