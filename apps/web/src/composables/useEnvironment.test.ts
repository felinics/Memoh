// @vitest-environment jsdom
import { createApp, defineComponent, h, nextTick } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'
import { DesktopShellKey } from '@/lib/desktop-shell'
import {
  ANY_COARSE_POINTER_MEDIA,
  ANY_FINE_POINTER_MEDIA,
  ANY_HOVER_MEDIA,
  CAN_HOVER_MEDIA,
  COARSE_POINTER_MEDIA,
  COMPACT_SHELL_MEDIA,
  REDUCED_MOTION_MEDIA,
  TOUCH_POINTER_MEDIA,
} from '@/lib/runtime-env'
import {
  useCompactShell,
  useDesktopShell,
  useInputModality,
  useReducedMotion,
  useTouchPointer,
} from './useEnvironment'

function installMediaHarness(initial: Record<string, boolean> = {}) {
  const values = new Map<string, boolean>(Object.entries(initial))
  const listeners = new Map<string, Set<(event: MediaQueryListEvent) => void>>()

  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: (query: string) => {
      if (!listeners.has(query)) listeners.set(query, new Set())
      const set = listeners.get(query)!
      return {
        media: query,
        get matches() {
          return values.get(query) ?? false
        },
        onchange: null,
        addListener: () => {},
        removeListener: () => {},
        addEventListener: (_type: string, cb: (event: MediaQueryListEvent) => void) => {
          set.add(cb)
        },
        removeEventListener: (_type: string, cb: (event: MediaQueryListEvent) => void) => {
          set.delete(cb)
        },
        dispatchEvent: () => true,
      }
    },
  })

  return {
    set(query: string, next: boolean) {
      values.set(query, next)
      for (const cb of [...(listeners.get(query) ?? [])]) {
        cb({ matches: next, media: query } as MediaQueryListEvent)
      }
    },
  }
}

describe('useEnvironment orthogonal layers', () => {
  const cleanups: Array<() => void> = []

  afterEach(() => {
    while (cleanups.length) cleanups.pop()!()
  })

  function mountProbe<T>(setup: () => T, provideDesktopShell = false): T {
    let captured!: T
    const host = document.createElement('div')
    const Probe = defineComponent({
      setup() {
        captured = setup()
        return () => h('div')
      },
    })
    const app = createApp(Probe)
    if (provideDesktopShell) app.provide(DesktopShellKey, true)
    app.mount(host)
    cleanups.push(() => app.unmount())
    return captured
  }

  it('keeps wide touch tablets (iPad / landscape phone) in wide layout while blocking auto-focus', () => {
    installMediaHarness({
      [COMPACT_SHELL_MEDIA]: false,
      [COARSE_POINTER_MEDIA]: true,
      [CAN_HOVER_MEDIA]: false,
      [TOUCH_POINTER_MEDIA]: true,
      [ANY_COARSE_POINTER_MEDIA]: true,
      // Even with an Apple Pencil paired (`any-pointer: fine`), primary pointer
      // stays coarse and focusing an input still raises the virtual keyboard.
      [ANY_FINE_POINTER_MEDIA]: true,
      [ANY_HOVER_MEDIA]: false,
    })

    const env = mountProbe(() => ({
      compactShell: useCompactShell(),
      touchPointer: useTouchPointer(),
      modality: useInputModality(),
      desktopShell: useDesktopShell(),
    }))

    expect(env.compactShell.value).toBe(false)
    expect(env.touchPointer.value).toBe(true)
    expect(env.modality.coarsePointer.value).toBe(true)
    expect(env.modality.anyFinePointer.value).toBe(true)
    expect(env.modality.canAutoFocusInput.value).toBe(false)
    expect(env.desktopShell).toBe(false)
  })

  it('models coexisting touch + mouse on touchscreen laptops without suppressing auto-focus', () => {
    installMediaHarness({
      [COMPACT_SHELL_MEDIA]: false,
      [COARSE_POINTER_MEDIA]: false,
      [CAN_HOVER_MEDIA]: true,
      [TOUCH_POINTER_MEDIA]: false,
      [ANY_COARSE_POINTER_MEDIA]: true,
      [ANY_FINE_POINTER_MEDIA]: true,
      [ANY_HOVER_MEDIA]: true,
    })

    const { modality } = mountProbe(() => ({ modality: useInputModality() }), true)
    expect(modality.coarsePointer.value).toBe(false)
    expect(modality.canHover.value).toBe(true)
    expect(modality.anyCoarsePointer.value).toBe(true)
    expect(modality.anyFinePointer.value).toBe(true)
    expect(modality.canAutoFocusInput.value).toBe(true)
  })

  it('reacts to live media changes and reads DesktopShellKey injection', async () => {
    const media = installMediaHarness({
      [COMPACT_SHELL_MEDIA]: true,
      [COARSE_POINTER_MEDIA]: false,
      [REDUCED_MOTION_MEDIA]: false,
    })

    const env = mountProbe(() => ({
      compactShell: useCompactShell(),
      modality: useInputModality(),
      reducedMotion: useReducedMotion(),
      desktopShell: useDesktopShell(),
    }), true)

    expect(env.desktopShell).toBe(true)
    expect(env.compactShell.value).toBe(true)
    expect(env.modality.canAutoFocusInput.value).toBe(true)
    expect(env.reducedMotion.value).toBe(false)

    media.set(COARSE_POINTER_MEDIA, true)
    media.set(REDUCED_MOTION_MEDIA, true)
    await nextTick()

    expect(env.modality.coarsePointer.value).toBe(true)
    expect(env.modality.canAutoFocusInput.value).toBe(false)
    expect(env.reducedMotion.value).toBe(true)
  })
})
