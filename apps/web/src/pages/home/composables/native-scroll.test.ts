// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nativeScrollTo } from './native-scroll'

afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

function viewport() {
  const root = document.createElement('div')
  Object.defineProperties(root, {
    scrollHeight: { value: 1000 },
    clientHeight: { value: 200 },
    onscrollend: { value: null },
  })
  root.scrollTop = 100
  root.scrollTo = vi.fn()
  return root
}

describe('native scrolling', () => {
  it('issues one browser scroll and finishes on scrollend without driving frames', () => {
    const root = viewport()
    const finish = vi.fn()
    const raf = vi.fn()
    vi.stubGlobal('requestAnimationFrame', raf)
    nativeScrollTo(root, 1200, finish)
    expect(root.scrollTo).toHaveBeenCalledExactlyOnceWith({ top: 800, behavior: 'smooth' })
    root.dispatchEvent(new Event('scroll'))
    expect(finish).not.toHaveBeenCalled()
    root.dispatchEvent(new Event('scrollend'))
    expect(finish).toHaveBeenCalledOnce()
    expect(raf).not.toHaveBeenCalled()
  })

  it('cancels at the current position and ignores subsequent completion events', () => {
    const root = viewport()
    const finish = vi.fn()
    const cancel = nativeScrollTo(root, 800, finish)
    root.scrollTop = 240
    cancel()
    cancel()
    root.dispatchEvent(new Event('scrollend'))
    expect(root.scrollTo).toHaveBeenLastCalledWith({ top: 240, behavior: 'instant' })
    expect(root.scrollTo).toHaveBeenCalledTimes(2)
    expect(finish).toHaveBeenCalledOnce()
  })

  it('releases a zero-distance scroll without waiting for an event', async () => {
    const root = viewport()
    const finish = vi.fn()
    nativeScrollTo(root, 100, finish)
    await Promise.resolve()
    expect(finish).toHaveBeenCalledOnce()
  })

  it('uses an instant jump for reduced motion', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true }))
    const root = viewport()
    const finish = vi.fn()
    nativeScrollTo(root, 500, finish)
    await Promise.resolve()
    expect(root.scrollTo).toHaveBeenCalledWith({ top: 500, behavior: 'instant' })
    expect(finish).toHaveBeenCalledOnce()
  })
})
