// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, ref, type App } from 'vue'
import { useAppPreparation, type AppPreparationTarget } from './useAppPreparation'

const mocks = vi.hoisted(() => ({ prepare: vi.fn() }))
vi.mock('@/composables/api/useApps', () => ({ prepareAppOperation: mocks.prepare }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/utils/api-error', () => ({ resolveApiErrorMessage: (_: unknown, fallback: string) => fallback }))

let app: App | undefined
const result = { registry_id: 'memoh', app_id: 'editor', revision: 'app-revision', dependencies: [
  { dependency_id: 'node', action: 'install', version: '22.1.0', definition_revision: 'recipe-1' },
] }

function setup() {
  const target = ref<AppPreparationTarget | null>({
    botId: 'bot-a',
    request: { action: 'update', registry_id: 'memoh', app_id: 'editor', release: true, dependencies: ['node'] },
  })
  let flow!: ReturnType<typeof useAppPreparation>
  app = createApp({ setup() { flow = useAppPreparation(() => target.value); return () => h('div') } })
  app.mount(document.createElement('div'))
  return { target, flow }
}

beforeEach(() => { mocks.prepare.mockReset(); mocks.prepare.mockResolvedValue(result) })
afterEach(() => { app?.unmount() })

describe('App dependency preparation', () => {
  it('waits for an explicit review and binds its snapshot to the selected bot and release', async () => {
    const { flow } = setup()
    expect(mocks.prepare).not.toHaveBeenCalled()
    await flow.prepare()
    expect(mocks.prepare).toHaveBeenCalledWith('bot-a', {
      action: 'update', registry_id: 'memoh', app_id: 'editor', release: true, dependencies: ['node'],
    }, expect.any(AbortSignal))
    expect(flow.prepared.value).toMatchObject({ botId: 'bot-a', result })
  })

  it.each(['bot', 'selection', 'close'] as const)('discards a late response after changing %s', async change => {
    let resolve!: (value: unknown) => void
    mocks.prepare.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const { target, flow } = setup()
    const pending = flow.prepare()
    const signal = mocks.prepare.mock.calls[0]![2] as AbortSignal
    if (change === 'bot') target.value!.botId = 'bot-b'
    if (change === 'selection' && target.value?.request.action === 'update') target.value.request.dependencies = []
    if (change === 'close') target.value = null
    expect(signal.aborted).toBe(true)
    resolve(result)
    await pending
    expect(flow.prepared.value).toBeNull()
    expect(flow.preparing.value).toBe(false)
  })

  it('requires fresh confirmation after reopening the same target', async () => {
    const { target, flow } = setup()
    await flow.prepare()
    const original = target.value
    target.value = null
    target.value = original
    expect(flow.prepared.value).toBeNull()
    expect(mocks.prepare).toHaveBeenCalledTimes(1)
  })

  it('ignores the first request when a new selection finishes sooner', async () => {
    let resolveOld!: (value: unknown) => void
    mocks.prepare.mockReturnValueOnce(new Promise(done => { resolveOld = done }))
    const { target, flow } = setup()
    const old = flow.prepare()
    target.value!.botId = 'bot-b'
    await flow.prepare()
    resolveOld({ ...result, revision: 'stale-revision' })
    await old
    expect(flow.prepared.value).toMatchObject({ botId: 'bot-b', result: { revision: 'app-revision' } })
  })

  it('coalesces a double click and leaves failed preparation retryable', async () => {
    let reject!: (cause: unknown) => void
    mocks.prepare.mockReturnValueOnce(new Promise((_, fail) => { reject = fail }))
    const { flow } = setup()
    const pending = flow.prepare()
    await flow.prepare()
    expect(mocks.prepare).toHaveBeenCalledTimes(1)
    reject(new Error('offline'))
    await pending
    expect(flow.prepared.value).toBeNull()
    expect(flow.error.value).toBe('apps.prepare.failed')
    await flow.prepare()
    expect(flow.prepared.value?.result).toEqual(result)
    expect(flow.error.value).toBe('')
  })
})
