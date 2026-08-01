import type { BrowserWindowConstructorOptions } from 'electron'

export function macWindowChromeOptions(
  platform: NodeJS.Platform,
  tabbingIdentifier: string,
): Partial<BrowserWindowConstructorOptions> {
  if (platform !== 'darwin') return {}
  return {
    titleBarStyle: 'hidden',
    trafficLightPosition: { x: 14, y: 13 },
    transparent: true,
    backgroundColor: '#00000000',
    // The native material fills the window; opaque renderer surfaces cover the
    // content pane, leaving only explicitly transparent sidebars visible.
    vibrancy: 'sidebar',
    visualEffectState: 'followWindow',
    tabbingIdentifier,
  }
}
