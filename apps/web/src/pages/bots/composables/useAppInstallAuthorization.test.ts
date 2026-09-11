// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref, type App } from 'vue'
import type { AppOperation } from '@/store/app-operations'
import { useAppInstallAuthorization } from './useAppInstallAuthorization'

const mocks = vi.hoisted(() => ({ list: vi.fn(), invalidate: vi.fn() }))
vi.mock('@memohai/sdk', () => ({ getBotsByBotIdApps: mocks.list }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@pinia/colada', () => ({ useQueryCache: () => ({ invalidateQueries: mocks.invalidate }) }))
vi.mock('@/composables/api/useApps', () => ({ invalidateBotApps: mocks.invalidate }))
vi.mock('@/utils/api-error', () => ({ resolveApiErrorMessage: (_: unknown, fallback: string) => fallback }))

let app: App | undefined
const flush = async () => { await Promise.resolve(); await nextTick(); await Promise.resolve(); await nextTick() }
const connector = (type: string, status = 'needs_auth', connectionStatus?: string) => ({
  type, required: true, status, connector: connectionStatus ? { status: connectionStatus } : undefined,
})
const item = (connectors: unknown[], status = 'partial') => ({
  installation_id: 'installation', registry_id: 'memoh', app_id: 'example', status, connectors,
})
function setup(status = 'running', action = 'install') {
  const operation = ref({
    key: 'bot/memoh/example', botId: 'bot', targetId: 'remote-target', registryId: 'memoh',
    appId: 'example', status, action, result: 'partial',
    steps: [{ kind: 'connector', id: 'notion', status: 'needs_auth' }],
  } as AppOperation)
  const open = ref(true)
  let flow!: ReturnType<typeof useAppInstallAuthorization>
  app = createApp({ setup() { flow = useAppInstallAuthorization(operation, open); return () => h('div') } })
  app.mount(document.createElement('div'))
  return { flow, operation, open }
}
afterEach(() => { app?.unmount(); vi.resetAllMocks() })

describe('installation authorization', () => {
  it('waits for installation and resolves the exact App and workspace target', async () => {
    mocks.list.mockResolvedValue({ data: { items: [item([connector('notion')])] } })
    const { flow, operation } = setup()
    expect(mocks.list).not.toHaveBeenCalled()
    operation.value.status = 'done'
    await flush()
    expect(mocks.list).toHaveBeenCalledWith(expect.objectContaining({ path: { bot_id: 'bot' }, query: { workspace_target_id: 'remote-target' } }))
    expect(flow.current.value?.type).toBe('notion')
    expect(flow.needsSetup.value).toBe(true)
    expect(operation.value.installationId).toBe('installation')
  })

  it('does not treat a linked pending OAuth connection as successful', async () => {
    mocks.list.mockResolvedValue({ data: { items: [item([connector('notion', 'linked', 'pending')], 'installed')] } })
    const { flow, operation } = setup('done')
    await flush()
    expect(flow.needsSetup.value).toBe(true)
    expect(operation.value.result).toBe('partial')
    expect(operation.value.steps[0]?.status).toBe('needs_auth')
  })

  it('advances through connectors and refreshes the installed result after authorization', async () => {
    mocks.list.mockResolvedValueOnce({ data: { items: [item([connector('notion'), connector('github')])] } })
    const { flow, operation } = setup('done')
    await flush()
    mocks.list.mockResolvedValueOnce({ data: { items: [item([connector('notion', 'linked', 'active'), connector('github')])] } })
    await flow.authorized()
    expect(flow.current.value?.type).toBe('github')
    expect(operation.value.steps[0]?.status).toBe('linked')
    mocks.list.mockResolvedValueOnce({ data: { items: [item([connector('notion', 'linked', 'active'), connector('github', 'linked', 'active')], 'installed')] } })
    await flow.authorized()
    expect(flow.needsSetup.value).toBe(false)
    expect(operation.value.result).toBe('installed')
    expect(mocks.invalidate).toHaveBeenCalled()
  })

  it('preserves a partial result caused by another component after authorization', async () => {
    mocks.list.mockResolvedValue({ data: { items: [item([connector('notion', 'linked', 'active')])] } })
    const { flow, operation } = setup('done')
    await flush()
    expect(flow.needsSetup.value).toBe(false)
    expect(operation.value.result).toBe('partial')
  })

  it('keeps failed discovery retryable and ignores a late response after closing', async () => {
    mocks.list.mockRejectedValueOnce(new Error('offline'))
    const { flow, open, operation } = setup('done')
    await flush()
    expect(flow.error.value).toBeTruthy()
    expect(flow.needsSetup.value).toBe(true)
    let resolve!: (value: unknown) => void
    mocks.list.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const retry = flow.refresh()
    open.value = false
    await nextTick()
    resolve({ data: { items: [item([connector('notion', 'linked', 'active')], 'installed')] } })
    await retry
    expect(flow.installation.value).toBeNull()
    expect(operation.value.result).toBe('partial')
  })

  it('never starts authorization for removal', async () => {
    const { flow } = setup('done', 'remove')
    await flush()
    expect(mocks.list).not.toHaveBeenCalled()
    expect(flow.needsSetup.value).toBe(false)
  })
})
