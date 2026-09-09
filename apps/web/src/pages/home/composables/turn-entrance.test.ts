// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { animate } from 'motion/mini'
import { animateTurnEntrance } from './turn-entrance'

vi.mock('motion/mini', () => ({ animate: vi.fn(() => ({ stop: vi.fn() })) }))

afterEach(() => { vi.clearAllMocks(); vi.unstubAllGlobals() })

function turn() {
  const outer = document.createElement('div')
  outer.style.minHeight = '400px'
  outer.innerHTML = '<div data-turn-motion><div>prompt</div><div>reply</div></div>'
  return { outer, inner: outer.firstElementChild as HTMLElement }
}

describe('latest-turn entrance', () => {
  it('moves the content only and restores styles when interrupted', () => {
    const { outer, inner } = turn()
    const finish = vi.fn()
    const cancel = animateTurnEntrance(outer, 240, finish)
    expect(inner.style.transform).toBe('translateY(240px)')
    expect(outer.style.minHeight).toBe('400px')
    expect(outer.style.overflow).toBe('clip')
    expect(animate).toHaveBeenCalledWith(inner, { transform: ['translateY(240px)', 'translateY(0px)'] }, expect.objectContaining({
      ease: [0.16, 1, 0.3, 1], duration: 0.4,
    }))
    cancel()
    cancel()
    expect(inner.style.transform).toBe('')
    expect(outer.style.overflow).toBe('')
    expect(outer.style.minHeight).toBe('400px')
    expect(finish).toHaveBeenCalledTimes(1)
  })

  it('finishes without adding a transform when reduced motion is requested', () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true }))
    const { outer, inner } = turn()
    const finish = vi.fn()
    animateTurnEntrance(outer, 240, finish)
    expect(animate).not.toHaveBeenCalled()
    expect(inner.style.transform).toBe('')
    expect(finish).toHaveBeenCalledOnce()
  })
})
