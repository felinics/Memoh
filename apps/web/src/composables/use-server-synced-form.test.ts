import { describe, expect, it, vi } from 'vitest'
import { nextTick, reactive, ref } from 'vue'
import { useServerSyncedRecord, useServerSyncedScalar } from './use-server-synced-form'

describe('useServerSyncedRecord', () => {
  it('populates the form from the server on first sync', () => {
    const source = ref({ id: 'a', config: { appId: 'x', secret: 's' } })
    const form = reactive<Record<string, unknown>>({})
    useServerSyncedRecord(form, {
      source: () => source.value,
      identity: s => s.id,
      server: s => ({ ...s.config }),
    })
    expect(form).toEqual({ appId: 'x', secret: 's' })
  })

  it('keeps edited fields but lets untouched fields track a background refresh', async () => {
    const source = ref({ id: 'a', config: { appId: 'x', secret: 's' } })
    const form = reactive<Record<string, unknown>>({})
    useServerSyncedRecord(form, {
      source: () => source.value,
      identity: s => s.id,
      server: s => ({ ...s.config }),
    })

    form.appId = 'user-typed'
    // Background refetch: fresh object identity, server moved on one field.
    source.value = { id: 'a', config: { appId: 'x', secret: 'rotated' } }
    await nextTick()

    expect(form.appId).toBe('user-typed')
    expect(form.secret).toBe('rotated')
  })

  it('hard-resets on identity change even when the form is dirty', async () => {
    const source = ref({ id: 'a', config: { appId: 'x' } })
    const form = reactive<Record<string, unknown>>({})
    useServerSyncedRecord(form, {
      source: () => source.value,
      identity: s => s.id,
      server: s => ({ ...s.config }),
    })

    form.appId = 'user-typed'
    source.value = { id: 'b', config: { appId: 'other' } }
    await nextTick()

    expect(form.appId).toBe('other')
  })

  it('deletes server-removed keys only when the user did not diverge on them', async () => {
    const source = ref<{ id: string, config: Record<string, string> }>({ id: 'a', config: { keep: 'k', gone: 'g', edited: 'e' } })
    const form = reactive<Record<string, unknown>>({})
    useServerSyncedRecord(form, {
      source: () => source.value,
      identity: s => s.id,
      server: s => ({ ...s.config }),
    })

    form.edited = 'user-typed'
    source.value = { id: 'a', config: { keep: 'k' } }
    await nextTick()

    expect(form).toEqual({ keep: 'k', edited: 'user-typed' })
  })

  it('markClean rebaselines the guard so the next refresh applies server values', async () => {
    const source = ref({ id: 'a', config: { appId: 'x' } })
    const form = reactive<Record<string, unknown>>({})
    const { synced, markClean } = useServerSyncedRecord(form, {
      source: () => source.value,
      identity: s => s.id,
      server: s => ({ ...s.config }),
    })

    form.appId = 'user-typed'
    markClean()
    expect(synced.value).toEqual({ appId: 'user-typed' })

    source.value = { id: 'a', config: { appId: 'server-normalized' } }
    await nextTick()
    expect(form.appId).toBe('server-normalized')
  })

  it('reports the hard-reset flag through onSync', async () => {
    const source = ref({ id: 'a', config: {} })
    const form = reactive<Record<string, unknown>>({})
    const onSync = vi.fn()
    useServerSyncedRecord(form, {
      source: () => source.value,
      identity: s => s.id,
      server: s => ({ ...s.config }),
      onSync,
    })
    expect(onSync).toHaveBeenLastCalledWith(true, expect.anything())

    source.value = { id: 'a', config: {} }
    await nextTick()
    expect(onSync).toHaveBeenLastCalledWith(false, expect.anything())
  })
})

describe('useServerSyncedScalar', () => {
  it('keeps a diverged value across refreshes but tracks the server when untouched', async () => {
    const source = ref({ id: 'a', name: 'first' })
    const target = ref('')
    useServerSyncedScalar(target, {
      source: () => source.value,
      identity: s => s.id,
      server: s => s.name,
    })
    expect(target.value).toBe('first')

    target.value = 'user-typed'
    source.value = { id: 'a', name: 'renamed' }
    await nextTick()
    expect(target.value).toBe('user-typed')
  })

  it('hard-resets on identity change', async () => {
    const source = ref({ id: 'a', name: 'first' })
    const target = ref('')
    useServerSyncedScalar(target, {
      source: () => source.value,
      identity: s => s.id,
      server: s => s.name,
    })

    target.value = 'user-typed'
    source.value = { id: 'b', name: 'other' }
    await nextTick()
    expect(target.value).toBe('other')
  })
})
