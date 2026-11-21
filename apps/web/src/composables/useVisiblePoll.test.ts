// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KeepAlive, createApp, defineComponent, effectScope, h, nextTick, ref } from 'vue'
import { useVisiblePoll } from './useVisiblePoll'

/**
 * Failure Modes covered by this behavioral suite:
 * - FM1: Poll fires prematurely on mount without `immediate: true`, or fails to fire at `intervalMs` cadence.
 * - FM2: Poll keeps firing while `document.visibilityState === 'hidden'`, fails to run an immediate catch-up
 *        on `'hidden' -> 'visible'` by default, or ignores `immediateOnResume: false`.
 * - FM3: `<KeepAlive>` deactivated child keeps polling in background, double-fires on initial mount, or fails
 *        to resume on `onActivated`.
 * - FM4: Reactive `enabled` turning `false` leaves interval active, or visibility resume fires while `enabled`
 *        is still `false`.
 * - FM5: Slow unresolved `Promise` allows overlapping interval ticks to pile up, or a rejected tick wedges
 *        the in-flight lock permanently.
 * - FM6: `effectScope.stop()` / unmount leaks interval timers or `visibilitychange` resume triggers.
 */

function setDocumentVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    get: () => state,
  })
  document.dispatchEvent(new Event('visibilitychange'))
}

describe('useVisiblePoll', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    setDocumentVisibility('visible')
  })

  afterEach(() => {
    setDocumentVisibility('visible')
    vi.useRealTimers()
  })

  it('fires on interval cadence and respects immediate option (FM1)', () => {
    const fn = vi.fn()
    const scope = effectScope()
    const poll = scope.run(() => useVisiblePoll(fn, 2_000))!

    expect(poll.isActive.value).toBe(true)
    expect(fn).not.toHaveBeenCalled()

    vi.advanceTimersByTime(1_999)
    expect(fn).not.toHaveBeenCalled()

    vi.advanceTimersByTime(1)
    expect(fn).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(4_000)
    expect(fn).toHaveBeenCalledTimes(3)
    scope.stop()

    const immediateFn = vi.fn()
    const immediateScope = effectScope()
    immediateScope.run(() => useVisiblePoll(immediateFn, 2_000, { immediate: true }))
    expect(immediateFn).toHaveBeenCalledTimes(1)
    immediateScope.stop()
  })

  it('pauses when document becomes hidden and catches up on visible resume unless immediateOnResume is false (FM2)', () => {
    const fn = vi.fn()
    const scope = effectScope()
    const poll = scope.run(() => useVisiblePoll(fn, 2_000))!

    vi.advanceTimersByTime(2_000)
    expect(fn).toHaveBeenCalledTimes(1)

    setDocumentVisibility('hidden')
    expect(poll.isActive.value).toBe(false)

    // Ten full intervals pass in the background: zero requests may fire.
    vi.advanceTimersByTime(20_000)
    expect(fn).toHaveBeenCalledTimes(1)

    // Returning to foreground triggers an immediate catch-up and resumes interval cadence.
    setDocumentVisibility('visible')
    expect(poll.isActive.value).toBe(true)
    expect(fn).toHaveBeenCalledTimes(2)

    vi.advanceTimersByTime(2_000)
    expect(fn).toHaveBeenCalledTimes(3)
    scope.stop()

    // Opt-out of immediate catch-up on visibility resume.
    const quietFn = vi.fn()
    const quietScope = effectScope()
    quietScope.run(() => useVisiblePoll(quietFn, 2_000, { immediateOnResume: false }))
    setDocumentVisibility('hidden')
    vi.advanceTimersByTime(6_000)
    setDocumentVisibility('visible')
    expect(quietFn).not.toHaveBeenCalled()

    vi.advanceTimersByTime(2_000)
    expect(quietFn).toHaveBeenCalledTimes(1)
    quietScope.stop()
  })

  it('stops polling on KeepAlive onDeactivated and resumes with catch-up on onActivated without double-firing on initial mount (FM3)', async () => {
    const fn = vi.fn()
    const showPoller = ref(true)

    const PollerChild = defineComponent({
      name: 'PollerChild',
      setup() {
        useVisiblePoll(fn, 2_000)
        return () => h('div', 'poller')
      },
    })

    const container = document.createElement('div')
    const app = createApp(() => h(KeepAlive, null, [
      showPoller.value ? h(PollerChild, { key: 'poller' }) : h('div', { key: 'other' }, 'other'),
    ]))
    app.mount(container)
    await nextTick()

    // Initial mount inside KeepAlive fires onActivated, which must NOT masquerade as a resume.
    expect(fn).not.toHaveBeenCalled()

    vi.advanceTimersByTime(2_000)
    expect(fn).toHaveBeenCalledTimes(1)

    // Switch away so PollerChild is deactivated (kept alive in memory).
    showPoller.value = false
    await nextTick()

    vi.advanceTimersByTime(10_000)
    expect(fn).toHaveBeenCalledTimes(1)

    // Reactivate PollerChild: catches up immediately and resumes periodic ticks.
    showPoller.value = true
    await nextTick()
    expect(fn).toHaveBeenCalledTimes(2)

    vi.advanceTimersByTime(2_000)
    expect(fn).toHaveBeenCalledTimes(3)

    app.unmount()
  })

  it('stops and resumes with reactive enabled ref and never fires visibility catch-up while disabled (FM4)', () => {
    const fn = vi.fn()
    const enabled = ref(false)
    const scope = effectScope()
    const poll = scope.run(() => useVisiblePoll(fn, 2_000, { enabled }))!

    expect(poll.isActive.value).toBe(false)
    vi.advanceTimersByTime(6_000)
    expect(fn).not.toHaveBeenCalled()

    // Visibility transitions while disabled must not leak a catch-up call.
    setDocumentVisibility('hidden')
    setDocumentVisibility('visible')
    expect(fn).not.toHaveBeenCalled()
    expect(poll.isActive.value).toBe(false)

    // Enabling starts the interval cleanly.
    enabled.value = true
    expect(poll.isActive.value).toBe(true)
    vi.advanceTimersByTime(2_000)
    expect(fn).toHaveBeenCalledTimes(1)

    // Disabling mid-flight stops future ticks immediately.
    enabled.value = false
    expect(poll.isActive.value).toBe(false)
    vi.advanceTimersByTime(6_000)
    expect(fn).toHaveBeenCalledTimes(1)

    scope.stop()
  })

  it('skips overlapping ticks while an async call is still in flight and recovers after settlement or rejection (FM5)', async () => {
    let resolveSlow!: () => void
    let rejectNext = false
    const fn = vi.fn(() => {
      if (rejectNext) {
        rejectNext = false
        return Promise.reject(new Error('transient network failure'))
      }
      return new Promise<void>((resolve) => {
        resolveSlow = resolve
      })
    })

    const scope = effectScope()
    scope.run(() => useVisiblePoll(fn, 1_000))

    // Tick 1 starts the slow request.
    vi.advanceTimersByTime(1_000)
    expect(fn).toHaveBeenCalledTimes(1)

    // Ticks 2, 3, 4 arrive while Tick 1 is unresolved: zero overlapping invocations.
    vi.advanceTimersByTime(3_000)
    expect(fn).toHaveBeenCalledTimes(1)

    // Settle Tick 1 and flush microtasks so the in-flight lock releases.
    resolveSlow()
    await Promise.resolve()
    await Promise.resolve()

    // Next tick fails with rejection; lock must still release cleanly for subsequent ticks.
    rejectNext = true
    vi.advanceTimersByTime(1_000)
    expect(fn).toHaveBeenCalledTimes(2)
    await Promise.resolve()
    await Promise.resolve()

    vi.advanceTimersByTime(1_000)
    expect(fn).toHaveBeenCalledTimes(3)
    resolveSlow()
    await Promise.resolve()

    scope.stop()
  })

  it('cleans up timers and ignores resume / manual calls after scope.stop() (FM6)', async () => {
    const fn = vi.fn()
    const scope = effectScope()
    const poll = scope.run(() => useVisiblePoll(fn, 1_000))!

    vi.advanceTimersByTime(1_000)
    expect(fn).toHaveBeenCalledTimes(1)

    scope.stop()
    expect(poll.isActive.value).toBe(false)

    vi.advanceTimersByTime(10_000)
    setDocumentVisibility('hidden')
    setDocumentVisibility('visible')
    poll.resume()
    await poll.trigger()
    vi.advanceTimersByTime(5_000)

    expect(fn).toHaveBeenCalledTimes(1)
  })
})
