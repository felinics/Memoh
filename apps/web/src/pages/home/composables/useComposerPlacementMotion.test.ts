// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { effectScope, nextTick, ref } from 'vue'
import { animate } from 'motion/mini'
import { useComposerPlacementMotion } from './useComposerPlacementMotion'

vi.mock('motion/mini', () => ({ animate: vi.fn(() => ({ stop: vi.fn() })) }))
afterEach(() => { vi.clearAllMocks(); vi.unstubAllGlobals() })

it('moves from the welcome position and cleans up when returning to welcome', async () => {
  const el = document.createElement('div')
  const welcome = ref(true)
  let top = 300
  el.getBoundingClientRect = () => ({ left: 0, top }) as DOMRect
  const scope = effectScope()
  scope.run(() => useComposerPlacementMotion(ref(el), welcome))
  welcome.value = false
  await nextTick()
  top = 700
  await nextTick()
  expect(el.style.transform).toBe('translate(0px, -400px)')
  expect(animate).toHaveBeenCalledWith(el, { transform: ['translate(0px, -400px)', 'translate(0px, 0px)'] }, expect.objectContaining({ duration: 0.4 }))
  welcome.value = true
  await nextTick()
  expect(el.style.transform).toBe('')
  scope.stop()
})

describe('motion accessibility', () => {
  it('keeps reduced motion instantaneous', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true }))
    const welcome = ref(true)
    const scope = effectScope()
    scope.run(() => useComposerPlacementMotion(ref(document.createElement('div')), welcome))
    welcome.value = false
    await nextTick()
    expect(animate).not.toHaveBeenCalled()
    scope.stop()
  })
})
