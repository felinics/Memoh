import { describe, expect, it } from 'vitest'
import { macWindowChromeOptions } from './window-chrome'

describe('macWindowChromeOptions', () => {
  it('uses the native sidebar material on macOS', () => {
    expect(macWindowChromeOptions('darwin', 'memoh-chat')).toMatchObject({
      backgroundColor: '#00000000',
      tabbingIdentifier: 'memoh-chat',
      titleBarStyle: 'hidden',
      transparent: true,
      vibrancy: 'sidebar',
      visualEffectState: 'followWindow',
    })
  })

  it('leaves other platforms on their standard opaque window chrome', () => {
    expect(macWindowChromeOptions('win32', 'memoh-chat')).toEqual({})
    expect(macWindowChromeOptions('linux', 'memoh-chat')).toEqual({})
  })
})
