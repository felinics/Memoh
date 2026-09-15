import { beforeEach, describe, expect, it, vi } from 'vitest'
import { streamAppOperation, type AppStreamOptions } from './useAppStream'

const mocks = vi.hoisted(() => ({ install: vi.fn(), update: vi.fn(), resume: vi.fn(), remove: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  postBotsByBotIdApps: mocks.install,
  postBotsByBotIdAppsUpdate: mocks.update,
  postBotsByBotIdAppsByInstallationIdResume: mocks.resume,
  deleteBotsByBotIdAppsByInstallationId: mocks.remove,
}))
vi.mock('./sse-error', () => ({
  fetchSSEProblem: vi.fn(), isSSEErrorEvent: vi.fn(), localizeSSEErrorEvent: vi.fn(), normalizeSSEFailure: vi.fn(),
}))

const confirmations = [{
  dependency_id: 'codex', action: 'install' as const, version: '0.154.0', definition_revision: 'recipe-1',
  registry_id: 'memoh', source_url: 'https://example.org/registry', manifest_digest: 'digest-1',
}]
beforeEach(() => {
  vi.clearAllMocks()
  for (const api of Object.values(mocks)) api.mockResolvedValue({ stream: (async function* () { yield { type: 'done', status: 'installed' } })() })
})

async function consume(options: AppStreamOptions) {
  for await (const event of streamAppOperation(options)) expect(event.type).toBe('done')
}

describe('frozen App dependency requests', () => {
  it('posts the reviewed install release and dependency confirmations to the chosen bot', async () => {
    await consume({ botId: 'bot-a', action: 'install', install: { registryId: 'memoh', appId: 'codex', revision: 'app-1' }, dependencyConfirmations: confirmations })
    expect(mocks.install).toHaveBeenCalledWith(expect.objectContaining({
      path: { bot_id: 'bot-a' },
      body: { registry_id: 'memoh', app_id: 'codex', revision: 'app-1', dependency_confirmations: confirmations },
    }))
  })

  it('keeps release update and dependency selection bound to the prepared release', async () => {
    await consume({ botId: 'bot-a', action: 'update', registryId: 'memoh', appId: 'codex', update: { release: true, dependencies: ['codex'], releaseRevision: 'app-2' }, dependencyConfirmations: confirmations })
    expect(mocks.update).toHaveBeenCalledWith(expect.objectContaining({ body: {
      registry_id: 'memoh', app_id: 'codex', release: true, dependencies: ['codex'], release_revision: 'app-2', dependency_confirmations: confirmations,
    } }))
  })

  it('sends the reviewed App revision when resuming an existing installation', async () => {
    await consume({ botId: 'bot-a', action: 'resume', installationId: 'installation', resumeRevision: 'app-1', dependencyConfirmations: confirmations })
    expect(mocks.resume).toHaveBeenCalledWith(expect.objectContaining({
      path: { bot_id: 'bot-a', installation_id: 'installation' },
      body: { revision: 'app-1', dependency_confirmations: confirmations },
    }))
  })
})
