import { describe, expect, it, vi } from 'vitest'
import { nextTick, reactive } from 'vue'
import { useAutosaveQueue, type AutosaveJob } from './use-autosave-queue'

type Form = { a: string, b: string, c: string }

function deferred() {
  let resolve!: () => void
  let reject!: (error: unknown) => void
  const promise = new Promise<void>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function setup(buildJobs: (changed: (keyof Form)[]) => AutosaveJob<Form>[], onDrained?: (keys: Set<keyof Form>) => void) {
  const form = reactive<Form>({ a: '', b: '', c: '' })
  const synced = reactive<Form>({ a: '', b: '', c: '' })
  const queue = useAutosaveQueue<Form>({ form, synced, buildJobs, onDrained })
  return { form, synced, queue }
}

describe('useAutosaveQueue', () => {
  it('batches same-tick changes into one pass and advances synced on success', async () => {
    const saved: string[] = []
    const { form, synced } = setup((changed) => {
      saved.push([...changed].sort().join(','))
      return changed.map((key) => ({ payload: { [key]: form[key] } as Partial<Form>, save: () => Promise.resolve() }))
    })
    form.a = '1'
    form.b = '2'
    await nextTick()
    await vi.waitFor(() => expect(synced.a).toBe('1'))
    expect(saved).toEqual(['a,b'])
    expect(synced.b).toBe('2')
  })

  it('serializes: an edit during an in-flight save runs exactly one follow-up pass', async () => {
    const gate = deferred()
    const saveA = vi.fn(() => gate.promise)
    const saveB = vi.fn(() => Promise.resolve())
    const { form, synced } = setup((changed) => changed.map((key) => ({
      payload: { [key]: form[key] } as Partial<Form>,
      save: key === 'a' ? saveA : saveB,
    })))
    form.a = '1'
    await nextTick()
    expect(saveA).toHaveBeenCalledTimes(1)
    // Edit while the first save is still in flight — must queue, not parallel-run.
    form.b = '2'
    await nextTick()
    expect(saveB).toHaveBeenCalledTimes(0)
    gate.resolve()
    await vi.waitFor(() => expect(synced.b).toBe('2'))
    expect(saveA).toHaveBeenCalledTimes(1)
    expect(saveB).toHaveBeenCalledTimes(1)
  })

  it('rolls back untouched fields on failure and does not retry until the next edit', async () => {
    const onError = vi.fn()
    const save = vi.fn<() => Promise<void>>(() => Promise.reject(new Error('offline')))
    const { form, synced } = setup((changed) => changed.map((key) => ({
      payload: { [key]: form[key] } as Partial<Form>,
      save,
      onError,
    })))
    synced.a = 'server'
    form.a = 'server'
    form.a = 'mine'
    await nextTick()
    await vi.waitFor(() => expect(onError).toHaveBeenCalled())
    expect(form.a).toBe('server')
    await nextTick()
    expect(save).toHaveBeenCalledTimes(1)
    // Next edit re-triggers a save.
    save.mockImplementation(() => Promise.resolve())
    form.a = 'again'
    await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(2))
  })

  it('keeps the newer user edit when a save fails mid-flight, then saves it on the follow-up pass', async () => {
    // First attempt rejects (the mid-flight edit); later attempts succeed.
    const save = vi.fn<() => Promise<void>>().mockRejectedValueOnce(new Error('offline')).mockResolvedValue(undefined)
    const { form, synced } = setup((changed) => changed.map((key) => ({
      payload: { [key]: form[key] } as Partial<Form>,
      save,
    })))
    form.a = 'first'
    await nextTick()
    form.a = 'second'
    await vi.waitFor(() => expect(synced.a).toBe('second'))
    expect(form.a).toBe('second')
    expect(save).toHaveBeenCalledTimes(2)
  })

  it('respects rollback: false (draft fields keep their text on failure)', async () => {
    const { form } = setup((changed) => changed.map((key) => ({
      payload: { [key]: form[key] } as Partial<Form>,
      save: () => Promise.reject(new Error('conflict')),
      rollback: false,
    })))
    form.a = 'draft'
    await nextTick()
    await nextTick()
    expect(form.a).toBe('draft')
  })

  it('skips the pass when buildJobs returns [] (e.g. availability check pending)', async () => {
    const buildJobs = vi.fn(() => [] as AutosaveJob<Form>[])
    const { form, synced } = setup(buildJobs)
    form.a = '1'
    await nextTick()
    expect(buildJobs).toHaveBeenCalled()
    expect(synced.a).toBe('')
  })

  it('reports saved keys through onDrained only when something was saved', async () => {
    const onDrained = vi.fn()
    const { form } = setup(
      (changed) => changed.map((key) => ({ payload: { [key]: form[key] } as Partial<Form>, save: () => Promise.resolve() })),
      onDrained,
    )
    form.a = '1'
    await vi.waitFor(() => expect(onDrained).toHaveBeenCalledTimes(1))
    expect([...onDrained.mock.calls[0]![0]!]).toEqual(['a'])
  })
})
