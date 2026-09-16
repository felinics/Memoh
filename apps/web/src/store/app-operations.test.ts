import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  stream: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@memohai/sdk', () => ({ getBotsByBotIdApps: mocks.list }))
vi.mock('@felinic/ui', () => ({ toast: { success: mocks.success, error: mocks.error, warning: mocks.warning } }))
vi.mock('@/i18n', () => ({ default: { global: { t: (key: string) => key } } }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn().mockResolvedValue(undefined) }) }))
vi.mock('@pinia/colada', () => ({ useQueryCache: () => ({ invalidateQueries: vi.fn() }) }))
vi.mock('@/lib/auth-session', () => ({ onAuthSessionCleared: vi.fn() }))
vi.mock('@/composables/api/useApps', () => ({
  invalidateBotApps: vi.fn(),
  appInProgress: (item: { status?: string }) => ['installing', 'updating', 'removing'].includes(item.status ?? ''),
}))
vi.mock('@/composables/api/useWorkspaceDependencies', () => ({ invalidateBotDependencies: vi.fn() }))
vi.mock('@/composables/api/useAppStream', () => ({ streamAppOperation: mocks.stream }))

import { useAppOperationsStore } from './app-operations'

const target = {
  botId: 'bot', registryId: 'memoh', appId: 'editor',
  installationId: 'installation', name: 'Editor', action: 'remove' as const,
}

describe('remove operation recovery after a lost stream', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
    vi.clearAllMocks()
    mocks.stream.mockImplementation(async function* () {
      yield { type: 'step', kind: 'connector', id: 'github' }
      // Proxy EOF without a terminal event.
    })
  })
  afterEach(() => {
    useAppOperationsStore().reset()
    vi.useRealTimers()
  })

  it('keeps failed removal as an error, including its persisted detail and retry action', async () => {
    mocks.list.mockResolvedValue({ data: { items: [{
      registry_id: 'memoh', app_id: 'editor', installation_id: 'installation',
      status: 'failed', last_error: 'App removal failed: disconnect unavailable',
    }] } })
    const store = useAppOperationsStore()
    const result = store.start(target)
    if (result.kind !== 'started') throw new Error('operation did not start')
    store.view(result.operation.key, 'test')
    await vi.advanceTimersByTimeAsync(3000)
    expect(result.operation).toMatchObject({
      status: 'error', result: '', error: 'App removal failed: disconnect unavailable',
    })
    expect(result.operation.steps[0]?.status).not.toBe('removed')
    expect(mocks.success).not.toHaveBeenCalled()
    expect(store.retry(result.operation.key)).toBe(true)
    expect(mocks.stream).toHaveBeenLastCalledWith(expect.objectContaining({ action: 'remove' }))
  })

  it('reports an unwatched failed removal through an error toast', async () => {
    mocks.list.mockResolvedValue({ data: { items: [{
      registry_id: 'memoh', app_id: 'editor', installation_id: 'installation', status: 'failed', last_error: 'cleanup failed',
    }] } })
    useAppOperationsStore().start(target)
    await vi.advanceTimersByTimeAsync(3000)
    expect(mocks.error).toHaveBeenCalledWith('apps.background.failed', expect.objectContaining({ description: 'cleanup failed' }))
    expect(mocks.success).not.toHaveBeenCalled()
  })

  it('only reports removed after the installation disappears', async () => {
    mocks.list.mockResolvedValueOnce({ data: { items: [{
      registry_id: 'memoh', app_id: 'editor', installation_id: 'installation', status: 'installed',
    }] } }).mockResolvedValue({ data: { items: [] } })
    const result = useAppOperationsStore().start(target)
    if (result.kind !== 'started') throw new Error('operation did not start')
    await vi.advanceTimersByTimeAsync(3000)
    expect(result.operation.status).toBe('running')
    expect(mocks.success).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(3000)
    expect(result.operation).toMatchObject({ status: 'done', result: 'removed' })
    expect(mocks.success).toHaveBeenCalledWith('apps.background.removed', expect.anything())
  })
})

describe('confirmed App dependency retry', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    mocks.stream.mockImplementation(async function* () {
      yield { type: 'error', code: 'workspace_dependency_install_failed', message: 'Installation failed' }
    })
  })
  afterEach(() => { useAppOperationsStore().reset() })

  it('keeps exact release, dependency version and recipe even when the caller changes its selection', async () => {
    const update = { release: true, dependencies: ['node'], releaseRevision: 'app-revision-1' }
    const confirmation = {
      dependency_id: 'node', action: 'update' as const, version: '22.1.0', definition_revision: 'recipe-1',
      registry_id: 'memoh', source_url: 'https://example.org/registry', manifest_digest: 'digest-1',
    }
    const store = useAppOperationsStore()
    const result = store.start({ ...target, action: 'update', update, dependencyConfirmations: [confirmation] })
    if (result.kind !== 'started') throw new Error('operation did not start')
    store.view(result.operation.key, 'test')
    await vi.waitFor(() => expect(result.operation.status).toBe('error'))

    update.dependencies.push('python')
    update.releaseRevision = 'app-revision-2'
    confirmation.version = '23.0.0'
    confirmation.definition_revision = 'recipe-2'
    expect(store.retry(result.operation.key)).toBe(true)
    expect(mocks.stream).toHaveBeenLastCalledWith(expect.objectContaining({
      update: { release: true, dependencies: ['node'], releaseRevision: 'app-revision-1' },
      dependencyConfirmations: [expect.objectContaining({ version: '22.1.0', definition_revision: 'recipe-1' })],
    }))
  })

  it('retries resume on the reviewed App revision and exact dependency confirmation', async () => {
    const store = useAppOperationsStore()
    const result = store.start({ ...target, action: 'resume', resumeRevision: 'app-revision-1', dependencyConfirmations: [] })
    if (result.kind !== 'started') throw new Error('operation did not start')
    store.view(result.operation.key, 'test')
    await vi.waitFor(() => expect(result.operation.status).toBe('error'))
    expect(store.retry(result.operation.key)).toBe(true)
    expect(mocks.stream).toHaveBeenLastCalledWith(expect.objectContaining({ resumeRevision: 'app-revision-1', dependencyConfirmations: [] }))
  })
})
