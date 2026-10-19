import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { BrowserWindow } from 'electron'

const mocks = vi.hoisted(() => ({
  is: { dev: false },
  watchWindowShortcuts: vi.fn(),
}))

vi.mock('@electron-toolkit/utils', () => ({
  is: mocks.is,
  optimizer: { watchWindowShortcuts: mocks.watchWindowShortcuts },
}))

import { attachDevelopmentWindowShortcuts } from './window-shortcuts'

describe('attachDevelopmentWindowShortcuts', () => {
  beforeEach(() => {
    mocks.is.dev = false
    mocks.watchWindowShortcuts.mockReset()
  })

  it('leaves native DevTools accelerators available in packaged builds', () => {
    const window = {} as BrowserWindow

    attachDevelopmentWindowShortcuts(window)

    expect(mocks.watchWindowShortcuts).not.toHaveBeenCalled()
  })

  it('keeps the development-only F12 helper', () => {
    const window = {} as BrowserWindow
    mocks.is.dev = true

    attachDevelopmentWindowShortcuts(window)

    expect(mocks.watchWindowShortcuts).toHaveBeenCalledWith(window)
  })
})
