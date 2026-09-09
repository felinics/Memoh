import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { treeSpinnerDelayMs, useTreeDisclosure } from './tree-disclosure'

function deferred() {
  let resolve!: (ok: boolean) => void
  const promise = new Promise<boolean>((r) => { resolve = r })
  return { promise, resolve }
}

describe('useTreeDisclosure', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('expands, loads once, and reuses the loaded children on re-expand', async () => {
    const load = vi.fn(async () => true)
    const d = useTreeDisclosure(load)

    await d.expand()
    expect(d.expanded.value).toBe(true)
    expect(d.loaded.value).toBe(true)
    expect(load).toHaveBeenCalledTimes(1)

    d.toggle()
    expect(d.expanded.value).toBe(false)
    d.toggle()
    expect(d.expanded.value).toBe(true)
    expect(load).toHaveBeenCalledTimes(1)
  })

  it('never shows the spinner when the load settles before the delay', async () => {
    const d = useTreeDisclosure(async () => true)

    await d.expand()
    expect(d.spinnerVisible.value).toBe(false)
    await vi.advanceTimersByTimeAsync(treeSpinnerDelayMs * 2)
    expect(d.spinnerVisible.value).toBe(false)
  })

  it('shows the spinner only after the delay, and clears it when the load settles', async () => {
    const gate = deferred()
    const d = useTreeDisclosure(() => gate.promise)

    const expanding = d.expand()
    expect(d.spinnerVisible.value).toBe(false)
    await vi.advanceTimersByTimeAsync(treeSpinnerDelayMs - 1)
    expect(d.spinnerVisible.value).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(d.spinnerVisible.value).toBe(true)

    gate.resolve(true)
    await expanding
    expect(d.spinnerVisible.value).toBe(false)
    expect(d.loaded.value).toBe(true)
  })

  it('hides the spinner when the node is collapsed mid-load', async () => {
    const gate = deferred()
    const d = useTreeDisclosure(() => gate.promise)

    const expanding = d.expand()
    await vi.advanceTimersByTimeAsync(treeSpinnerDelayMs)
    expect(d.spinnerVisible.value).toBe(true)

    d.toggle()
    expect(d.spinnerVisible.value).toBe(false)

    gate.resolve(true)
    await expanding
    expect(d.expanded.value).toBe(false)
    expect(d.loaded.value).toBe(true)
  })

  it('stays unloaded when the load reports failure, so expand retries', async () => {
    const load = vi.fn(async () => false)
    const d = useTreeDisclosure(load)

    await d.expand()
    expect(d.loaded.value).toBe(false)
    expect(d.spinnerVisible.value).toBe(false)

    await d.expand()
    expect(load).toHaveBeenCalledTimes(2)
  })

  it('reload refreshes in place: loaded stays true and the spinner overlays after the delay', async () => {
    let gate: ReturnType<typeof deferred> | null = null
    const load = vi.fn(() => gate ? gate.promise : Promise.resolve(true))
    const d = useTreeDisclosure(load)

    await d.expand()
    expect(d.loaded.value).toBe(true)

    gate = deferred()
    const reloading = d.reload()
    await vi.advanceTimersByTimeAsync(treeSpinnerDelayMs)
    expect(d.spinnerVisible.value).toBe(true)
    expect(d.loaded.value).toBe(true)
    expect(d.expanded.value).toBe(true)

    gate.resolve(true)
    await reloading
    expect(d.spinnerVisible.value).toBe(false)
    expect(load).toHaveBeenCalledTimes(2)
  })
})
