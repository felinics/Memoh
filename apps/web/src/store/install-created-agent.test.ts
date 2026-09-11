import { beforeEach, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
const api = vi.hoisted(() => ({ preflight: vi.fn(), app: vi.fn(), startWorkspace: vi.fn(), start: vi.fn(), view: vi.fn(), unview: vi.fn() }))
vi.mock('@memohai/sdk', () => ({ getSupermarketRegistriesByRegistryIdAppsByAppId: api.app, postBotsByBotIdContainerStart: api.startWorkspace }))
vi.mock('@/composables/api/useWorkspaceDependencies', () => ({ preflightDependencies: api.preflight }))
vi.mock('@/i18n', () => ({ default: { global: { t: (key: string) => key } } }))
vi.mock('./app-operations', () => ({ useAppOperationsStore: () => ({ start: api.start, view: api.view, unview: api.unview }) }))
import { installCreatedAgent } from './install-created-agent'
const agent = { id: 'agent', runtime: 'codex', dependency: { dependency_id: 'codex' } }
const satisfied = { workspace_state: 'running', items: [{ dependency_id: 'codex', state: 'satisfied' }] }
let operation: { key: string, status: string, result: string, error: string }
beforeEach(() => {
  vi.resetAllMocks()
  operation = reactive({ key: 'bot/memoh/codex', status: 'running', result: '', error: '' })
  api.preflight.mockResolvedValueOnce({ workspace_state: 'running', items: [{ dependency_id: 'codex', state: 'missing' }] }).mockResolvedValue(satisfied)
  api.app.mockResolvedValue({ data: { revision: 'published-revision' } })
  api.start.mockReturnValue({ kind: 'started', operation })
})

it('installs the canonical App automatically and waits for a verified dependency', async () => {
  const started = Promise.withResolvers<void>()
  api.view.mockImplementation(() => started.resolve())
  const installing = installCreatedAgent('bot', agent)
  await started.promise
  expect(api.start).toHaveBeenCalledWith(expect.objectContaining({
    action: 'install', install: { registryId: 'memoh', appId: 'codex', revision: 'published-revision' },
  }))
  operation.result = 'installed'; operation.status = 'done'
  await installing
  expect(api.preflight).toHaveBeenCalledTimes(2)
  expect(api.unview).toHaveBeenCalledWith(operation.key, 'bot-create-progress')
})

it('does not update a runtime that is already installed', async () => {
  api.preflight.mockReset().mockResolvedValue(satisfied)
  await installCreatedAgent('bot', agent)
  expect(api.start).not.toHaveBeenCalled()
  expect(api.app).not.toHaveBeenCalled()
})

it('starts a stopped workspace and rechecks before enabling an existing runtime', async () => {
  api.preflight.mockReset().mockResolvedValueOnce({ workspace_state: 'not_running' }).mockResolvedValue(satisfied)
  await installCreatedAgent('bot', agent)
  expect(api.startWorkspace).toHaveBeenCalledWith({ path: { bot_id: 'bot' }, throwOnError: true })
  expect(api.preflight).toHaveBeenCalledTimes(2)
})

it.each(['error', 'partial'])('does not accept a failed or partial installation: %s', async (result) => {
  const started = Promise.withResolvers<void>()
  api.view.mockImplementation(() => started.resolve())
  const installing = installCreatedAgent('bot', agent)
  const rejected = expect(installing).rejects.toThrow()
  await started.promise
  operation.result = result; operation.status = result === 'error' ? 'error' : 'done'
  await rejected
  expect(api.unview).toHaveBeenCalled()
})

it('rejects an unavailable platform without launching installation', async () => {
  api.preflight.mockReset().mockResolvedValue({ workspace_state: 'running', items: [{ dependency_id: 'codex', state: 'platform_unsupported' }] })
  await expect(installCreatedAgent('bot', agent)).rejects.toThrow('bots.dependencies.preflight.platformUnsupported')
  expect(api.start).not.toHaveBeenCalled()
})
