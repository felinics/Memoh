// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { BotCreateStreamEvent } from '@/composables/api/useBotCreateStream'
import { readCreatedBotSession } from '@/pages/bots/created-bot-session'

const postBotsStream = vi.fn()
const postContainerStream = vi.fn()
const postBotsByBotIdAgents = vi.fn()
const claimCredential = vi.fn()
const putBotsByBotIdSettings = vi.fn()
const installAgent = vi.fn()
const patchAgent = vi.fn()
const getAgent = vi.fn()
const getAgents = vi.fn()
const getAuthorization = vi.fn()
const getBot = vi.fn()
const getBotChecks = vi.fn()
const grantAccess = vi.fn()
vi.mock('./install-created-agent', () => ({ installCreatedAgent: (...args: unknown[]) => installAgent(...args) }))

vi.mock('@/composables/api/useBotCreateStream', async (importActual) => {
  const actual = await importActual<typeof import('@/composables/api/useBotCreateStream')>()
  return { ...actual, postBotsStream: (...args: unknown[]) => postBotsStream(...args) }
})

vi.mock('@/composables/api/useContainerStream', () => ({
  postBotsByBotIdContainerStream: (...args: unknown[]) => postContainerStream(...args),
}))

vi.mock('@memohai/sdk', () => ({
  patchBotsByBotIdAgentsById: (...args: unknown[]) => patchAgent(...args),
  getBotsByBotIdAgentsById: (...args: unknown[]) => getAgent(...args),
  getBotsByBotIdAgents: (...args: unknown[]) => getAgents(...args),
  getAgentAuthorizationsById: (...args: unknown[]) => getAuthorization(...args),
  getBotsById: (...args: unknown[]) => getBot(...args),
  getBotsByIdChecks: (...args: unknown[]) => getBotChecks(...args),
  postBotsByBotIdAgents: (...args: unknown[]) => postBotsByBotIdAgents(...args),
  postBotsByBotIdAgentsByIdCredentialClaim: (...args: unknown[]) => claimCredential(...args),
  postBotsByBotIdUserAccess: (...args: unknown[]) => grantAccess(...args),
  putBotsByBotIdSettings: (...args: unknown[]) => putBotsByBotIdSettings(...args),
}))

const { useBotCreateProgressStore, BOT_STATUS_POLL_INTERVAL_MS } = await import('./bot-create-progress')

function streamOf(events: BotCreateStreamEvent[]) {
  return {
    stream: (async function* () {
      for (const event of events) yield event
    })(),
  }
}

function brokenStreamOf(events: BotCreateStreamEvent[], error: unknown) {
  return {
    stream: (async function* (): AsyncGenerator<BotCreateStreamEvent, void, unknown> {
      for (const event of events) yield event
      throw error
    })(),
  }
}

describe('useBotCreateProgressStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    sessionStorage.clear()
    installAgent.mockReset().mockResolvedValue(undefined)
    patchAgent.mockReset().mockResolvedValue({})
    getAgent.mockReset().mockResolvedValue({ data: { id: 'agent-1', runtime: 'codex', enabled: false, agent_credential_id: 'credential-1' } })
    getAgents.mockReset().mockResolvedValue({ data: { items: [] } })
    getAuthorization.mockReset().mockResolvedValue({ data: { auth_kind: 'openai_codex_oauth' } })
    getBot.mockReset().mockResolvedValue({ data: { id: 'bot-1', name: 'ada', display_name: 'Ada', status: 'ready' } })
    getBotChecks.mockReset().mockResolvedValue({ data: { items: [] } })
    grantAccess.mockReset().mockResolvedValue({ data: {} })
    postBotsStream.mockReset()
    postContainerStream.mockReset()
    postBotsByBotIdAgents.mockReset()
    claimCredential.mockReset()
    claimCredential.mockResolvedValue({ data: { id: 'credential-1' } })
    putBotsByBotIdSettings.mockReset()
    postBotsByBotIdAgents.mockResolvedValue({ data: { id: 'agent-1' } })
    putBotsByBotIdSettings.mockResolvedValue({ data: {} })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('streams the happy path to a ready state with a terminal log', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'pulling', image: 'img' },
      { type: 'pull_progress', layers: [{ ref: 'a', offset: 100, total: 100 }] },
      { type: 'creating' },
      {
        type: 'complete',
        container: {
          container_id: 'workspace-bot-1',
          workspace_backend: 'container',
          runtime_backend: 'io.containerd.runc.v2',
          started: true,
        },
      },
      { type: 'ready', bot },
    ]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('ready')
    expect(store.bot).toEqual(bot)
    expect(store.lines.map(l => l.kind)).toEqual(['command', 'bot-created', 'pulling', 'creating', 'ready'])
    expect(store.lines.at(-1)).toMatchObject({ kind: 'ready', status: 'done' })
  })

  it('treats a hard failure with no bot as an error status', async () => {
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'pulling', image: 'img' },
      { type: 'error', message: 'image pull failed' },
    ]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('error')
    expect(store.bot).toBeNull()
    expect(store.setupError).toBe('image pull failed')
    expect(store.lines.at(-1)).toMatchObject({ kind: 'error', status: 'error', message: 'image pull failed' })
  })

  it('keeps a Bot whose workspace failed as a workspace error instead of moving on', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'creating' },
      { type: 'error', code: 'workspace_setup_failed', message: 'container setup failed' },
    ]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' }, { settings: { chat_model_id: 'm1' } })

    expect(store.status).toBe('workspace-error')
    expect(store.bot).toEqual(bot)
    expect(store.setupError).toBe('container setup failed')
    expect(store.errorCode).toBe('workspace_setup_failed')
    expect(store.canRetry).toBe(true)
    // Settings wait for a workspace; the Bot itself is not declared ready.
    expect(putBotsByBotIdSettings).not.toHaveBeenCalled()
    expect(store.lines.at(-1)).toMatchObject({ kind: 'error', status: 'error', message: 'container setup failed' })
    // The created Bot is persisted for a refresh even without an Agent.
    expect(readCreatedBotSession()).toMatchObject({ botId: 'bot-1', botName: 'ada', displayName: 'Ada', settings: { chat_model_id: 'm1' } })
  })

  it('retries a failed workspace on the same Bot and then finishes the setup', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'error', code: 'workspace_setup_failed', message: 'container setup failed' },
    ]))
    postContainerStream.mockResolvedValue(streamOf([
      { type: 'pulling', image: 'img' },
      { type: 'creating' },
      { type: 'complete', container: { container_id: 'workspace-bot-1', started: true } },
    ]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' }, {
      settings: { chat_model_id: 'm1' },
      grants: [{ subject_type: 'user', user_id: 'u2', permissions: ['chat'] }],
    })
    expect(store.status).toBe('workspace-error')

    const result = await store.retry()

    expect(postBotsStream).toHaveBeenCalledTimes(1)
    expect(postContainerStream).toHaveBeenCalledWith(expect.objectContaining({ path: { bot_id: 'bot-1' } }))
    expect(grantAccess).toHaveBeenCalledTimes(1)
    expect(putBotsByBotIdSettings).toHaveBeenCalledWith(expect.objectContaining({ path: { bot_id: 'bot-1' }, body: { chat_model_id: 'm1' } }))
    expect(result?.settingsApplied).toBe(true)
    expect(store.status).toBe('ready')
    expect(store.setupError).toBeNull()
    expect(store.lines.map(l => l.kind)).toEqual(['command', 'bot-created', 'error', 'pulling', 'creating', 'applying-settings', 'ready'])
  })

  it('stays in the workspace error when the retried workspace fails again', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'error', code: 'workspace_setup_failed', message: 'first failure' },
    ]))
    postContainerStream.mockResolvedValue(streamOf([
      { type: 'creating' },
      { type: 'error', code: 'workspace_setup_failed', message: 'second failure' },
    ]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })
    await store.retry()

    expect(store.status).toBe('workspace-error')
    expect(store.setupError).toBe('second failure')
    expect(store.bot).toEqual(bot)
  })

  it('follows the Bot through its status when the create stream breaks without a server error', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(brokenStreamOf([{ type: 'bot_created', bot }, { type: 'creating' }], new Error('connection reset')))
    getBot.mockResolvedValue({ data: { ...bot, status: 'ready' } })

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(getBot).toHaveBeenCalledWith(expect.objectContaining({ path: { id: 'bot-1' } }))
    expect(postBotsStream).toHaveBeenCalledTimes(1)
    expect(store.status).toBe('ready')
    expect(store.setupError).toBeNull()
  })

  it('rethrows-as-error when the stream fails before any bot is created', async () => {
    postBotsStream.mockResolvedValue({
      stream: (async function* (): AsyncGenerator<BotCreateStreamEvent, void, unknown> {
        throw new Error('connection reset')
      })(),
    })

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('error')
    expect(store.bot).toBeNull()
    expect(store.setupError).toBe('connection reset')
    expect(store.lines.at(-1)).toMatchObject({ kind: 'error', status: 'error' })
  })

  it('resends a create whose response was lost under the same Idempotency-Key', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream
      .mockResolvedValueOnce(brokenStreamOf([], new Error('Failed to fetch')))
      .mockResolvedValueOnce(streamOf([{ type: 'bot_created', bot }, { type: 'ready', bot }]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })
    expect(store.status).toBe('error')
    await store.retry()

    const [first, resent] = postBotsStream.mock.calls.map(([options]) => options.headers['Idempotency-Key'])
    expect(first).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    expect(resent).toBe(first)
    expect(store.status).toBe('ready')
    expect(store.bot).toEqual(bot)
  })

  it('gives every create from the form its own Idempotency-Key', async () => {
    postBotsStream.mockImplementation(async () => brokenStreamOf([], new Error('Failed to fetch')))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })
    store.reset()
    await store.start({ name: 'ada', display_name: 'Ada' })

    const [first, second] = postBotsStream.mock.calls.map(([options]) => options.headers['Idempotency-Key'])
    expect(second).not.toBe(first)
  })

  it('keeps the stable code when bot creation returns an HTTP problem', async () => {
    postBotsStream.mockResolvedValue({
      stream: (async function* (): AsyncGenerator<BotCreateStreamEvent, void, unknown> {
        throw {
          code: 'bot.name_taken',
          args: { field: 'name' },
          detail: 'This name is already taken.',
          status: 409,
        }
      })(),
    })

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('error')
    expect(store.errorCode).toBe('bot.name_taken')
    expect(store.setupError).toBe('This name is already taken.')
    expect(store.canRetry).toBe(true)
  })

  it('recovers the name conflict from a legacy code-less 409 rejection', async () => {
    postBotsStream.mockResolvedValue({
      stream: (async function* (): AsyncGenerator<BotCreateStreamEvent, void, unknown> {
        // Older hosted servers reject with echo's plain body; fetchSSEProblem
        // attaches the HTTP status but there is no stable code to parse.
        throw { message: 'bot name already taken', status: 409 }
      })(),
    })

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('error')
    expect(store.errorCode).toBe('bot.name_taken')
    expect(store.setupError).toBe('bot name already taken')
  })

  it('applies model and memory settings after the bot is ready', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'ready', bot },
    ]))

    const store = useBotCreateProgressStore()
    const result = await store.start(
      { name: 'ada', display_name: 'Ada' },
      { settings: { chat_model_id: 'm1', memory_provider_id: 'p1' } },
    )

    expect(putBotsByBotIdSettings).toHaveBeenCalledWith(expect.objectContaining({
      path: { bot_id: 'bot-1' },
      body: { chat_model_id: 'm1', memory_provider_id: 'p1' },
    }))
    expect(store.status).toBe('ready')
    expect(result.settingsApplied).toBe(true)
    expect(store.lines.some(l => l.kind === 'applying-settings' && l.status === 'done')).toBe(true)
  })

  it('keeps the bot ready when settings application fails', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'ready', bot },
    ]))
    putBotsByBotIdSettings.mockRejectedValue(new Error('settings boom'))

    const store = useBotCreateProgressStore()
    const result = await store.start(
      { name: 'ada', display_name: 'Ada' },
      { settings: { chat_model_id: 'm1' } },
    )

    expect(store.status).toBe('ready')
    expect(store.bot).toEqual(bot)
    expect(result.settingsApplied).toBe(false)
    expect(store.lines.some(l => l.kind === 'applying-settings' && l.status === 'error')).toBe(true)
  })

  it('automatically installs a direct Agent before enabling it and declaring the Bot ready', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'ready', bot },
    ]))

    const store = useBotCreateProgressStore()
    const result = await store.start(
      { name: 'ada', display_name: 'Ada' },
      { agent: { name: 'Codex', provider: 'CODEX' } },
    )

    expect(postBotsByBotIdAgents).toHaveBeenCalledWith(expect.objectContaining({
      path: { bot_id: 'bot-1' },
      body: {
        name: 'Codex',
        // codex is a direct runtime; only non-direct providers create acp rows.
        runtime: 'codex',
        enabled: false,
        metadata: { provider: 'codex' },
      },
    }))
    expect(installAgent).toHaveBeenCalledWith('bot-1', expect.objectContaining({ id: 'agent-1', runtime: 'codex' }))
    expect(patchAgent).toHaveBeenCalledWith(expect.objectContaining({ body: { enabled: true } }))
    expect(patchAgent.mock.invocationCallOrder[0]).toBeGreaterThan(installAgent.mock.invocationCallOrder[0]!)
    expect(putBotsByBotIdSettings).toHaveBeenCalledWith(expect.objectContaining({ body: { default_bot_agent_id: 'agent-1' } }))
    expect(result.agentApplied).toBe(true)
    expect(store.lines.map(line => line.kind)).toEqual(['command', 'bot-created', 'applying-settings', 'installing-agent', 'ready'])
    expect(store.createdAgent).toMatchObject({ id: 'agent-1', runtime: 'codex' })
    expect(result.agentId).toBe('agent-1')
    expect(store.status).toBe('ready')
  })

  it.each([false, true])('binds the staged authorization and preserves the created Bot on claim failure: %s', async (failClaim) => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([{ type: 'ready', bot }]))
    if (failClaim) claimCredential.mockRejectedValue(new Error('claim unavailable'))
    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada' }, { agent: { name: 'Codex', provider: 'codex', authorizationId: 'staged-1' } })
    expect(claimCredential.mock.invocationCallOrder[0]).toBeGreaterThan(postBotsByBotIdAgents.mock.invocationCallOrder[0]!)
    expect(claimCredential).toHaveBeenCalledWith(expect.objectContaining({
      path: { bot_id: 'bot-1', id: 'agent-1' }, body: { authorization_id: 'staged-1' },
    }))
    expect(store.status).toBe(failClaim ? 'setup-error' : 'ready')
    expect(store.authorizationId).toBe('staged-1')
    expect(store.createdAgent?.id).toBe('agent-1')
    expect(store.createdAgent?.agent_credential_id).toBe(failClaim ? undefined : 'credential-1')
    expect(store.setupError).toBe(failClaim ? 'claim unavailable' : null)
    if (failClaim) {
      expect(installAgent).not.toHaveBeenCalled()
      expect(putBotsByBotIdSettings).not.toHaveBeenCalled()
    } else expect(putBotsByBotIdSettings).toHaveBeenCalled()
  })

  it('does not report the Agent as applied when selecting it as default fails', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'ready', bot },
    ]))
    putBotsByBotIdSettings.mockRejectedValue(new Error('default boom'))

    const store = useBotCreateProgressStore()
    const result = await store.start(
      { name: 'ada', display_name: 'Ada' },
      { agent: { name: 'Custom', provider: 'custom' } },
    )

    expect(result.agentApplied).toBe(false)
    expect(store.status).toBe('ready')
    expect(store.lines.some(l => l.kind === 'applying-settings' && l.status === 'error')).toBe(true)
  })

  it('keeps the created Bot for retry when setup fails when Agent creation fails', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'ready', bot },
    ]))
    postBotsByBotIdAgents.mockRejectedValue(new Error('agent boom'))

    const store = useBotCreateProgressStore()
    const result = await store.start(
      { name: 'ada', display_name: 'Ada' },
      { agent: { name: 'Codex', provider: 'codex' } },
    )

    expect(result.agentApplied).toBe(false)
    expect(store.status).toBe('setup-error')
    expect(store.setupError).toBe('agent boom')
    expect(store.lines.some(l => l.kind === 'applying-settings' && l.status === 'error')).toBe(true)
  })

  it.each(['codex', 'claude-code'])('waits for %s installation in the terminal and retries on the same Bot', async (runtime) => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([{ type: 'bot_created', bot }, { type: 'ready', bot }]))
    let failInstall!: (error: Error) => void
    const installing = Promise.withResolvers<void>()
    installAgent.mockImplementationOnce(() => {
      installing.resolve()
      return new Promise((_, reject) => { failInstall = reject })
    })
    const store = useBotCreateProgressStore()
    const creation = store.start({ name: 'ada' }, { settings: { memory_provider_id: 'memory-1' }, agent: { name: runtime, provider: runtime, authorizationId: 'stage' } })
    await installing.promise
    expect(store.status).toBe('creating')
    expect(store.lines.at(-1)).toMatchObject({ kind: 'installing-agent', status: 'running', message: runtime === 'codex' ? 'Codex' : 'Claude Code' })
    expect(store.lines.some(line => line.kind === 'ready')).toBe(false)
    expect(patchAgent).not.toHaveBeenCalled()
    failInstall(new Error('download interrupted'))
    await creation
    expect(store.status).toBe('setup-error')
    expect(store.lines.some(line => line.kind === 'installing-agent' && line.status === 'error')).toBe(true)
    getAgent.mockResolvedValue({ data: { id: 'agent-1', runtime, enabled: false, agent_credential_id: 'credential-1' } })
    await store.retry()
    expect(postBotsStream).toHaveBeenCalledTimes(1)
    expect(postContainerStream).not.toHaveBeenCalled()
    expect(postBotsByBotIdAgents).toHaveBeenCalledTimes(1)
    expect(claimCredential).toHaveBeenCalledTimes(1)
    expect(store.status).toBe('ready')
    expect(store.lines.at(-1)?.kind).toBe('ready')
  })

  it('resumes the saved Agent instead of creating another Bot after a refresh', async () => {
    const store = useBotCreateProgressStore()
    await store.restore({ botId: 'bot-1', botName: 'cat', displayName: 'Cat', runtime: 'codex', agentId: 'agent-1', authorizationId: 'stage', setupError: null })
    expect(store.status).toBe('ready')
    expect(getBot).toHaveBeenCalledWith(expect.objectContaining({ path: { id: 'bot-1' } }))
    expect(postBotsStream).not.toHaveBeenCalled()
    expect(postBotsByBotIdAgents).not.toHaveBeenCalled()
    expect(installAgent).toHaveBeenCalled()
    expect(store.createdAgent?.enabled).toBe(true)
  })

  it('resumes a plain Memoh Bot after a refresh and applies its pending settings', async () => {
    const store = useBotCreateProgressStore()
    await store.restore({ botId: 'bot-1', botName: 'cat', displayName: 'Cat', setupError: null, settings: { chat_model_id: 'm1' } }, true)
    expect(postBotsStream).not.toHaveBeenCalled()
    expect(postContainerStream).not.toHaveBeenCalled()
    expect(putBotsByBotIdSettings).toHaveBeenCalledWith(expect.objectContaining({ path: { bot_id: 'bot-1' }, body: { chat_model_id: 'm1' } }))
    expect(store.status).toBe('ready')
    expect(store.modelConfigured).toBe(true)
    expect(store.lines.map(l => l.kind)).toEqual(['bot-created', 'creating', 'applying-settings', 'ready'])
  })

  it('keeps polling a Bot that is still creating until the server settles it', async () => {
    vi.useFakeTimers()
    getBot
      .mockResolvedValueOnce({ data: { id: 'bot-1', status: 'creating' } })
      .mockResolvedValueOnce({ data: { id: 'bot-1', status: 'creating' } })
      .mockResolvedValue({ data: { id: 'bot-1', name: 'cat', status: 'ready' } })
    const store = useBotCreateProgressStore()
    const restoring = store.restore({ botId: 'bot-1', botName: 'cat', displayName: 'Cat', setupError: null })
    // Two observations of `creating` (now and one interval later) keep waiting.
    await vi.advanceTimersByTimeAsync(BOT_STATUS_POLL_INTERVAL_MS)
    expect(getBot).toHaveBeenCalledTimes(2)
    expect(store.status).toBe('creating')
    expect(store.lines.at(-1)).toMatchObject({ kind: 'creating', status: 'running' })
    await vi.advanceTimersByTimeAsync(BOT_STATUS_POLL_INTERVAL_MS)
    await restoring
    expect(getBot).toHaveBeenCalledTimes(3)
    expect(store.status).toBe('ready')
  })

  it('shows the failed workspace with its check detail after a refresh', async () => {
    getBot.mockResolvedValue({ data: { id: 'bot-1', name: 'cat', display_name: 'Cat', status: 'failed' } })
    getBotChecks.mockResolvedValue({ data: { items: [
      { type: 'container.init', status: 'error', detail: 'image pull failed: no space left on device' },
      { type: 'container.record', status: 'unknown', detail: 'pending' },
    ] } })
    const store = useBotCreateProgressStore()
    await store.restore({ botId: 'bot-1', botName: 'cat', displayName: '', setupError: null })
    expect(store.status).toBe('workspace-error')
    expect(store.setupError).toBe('image pull failed: no space left on device')
    expect(store.display?.display_name).toBe('Cat')
    expect(store.canRetry).toBe(true)
    expect(putBotsByBotIdSettings).not.toHaveBeenCalled()
    expect(store.lines.at(-1)).toMatchObject({ kind: 'error', message: 'image pull failed: no space left on device' })
  })

  it('drops a saved Bot that no longer exists and offers the form again', async () => {
    getBot.mockRejectedValue({ status: 404, message: 'bot not found' })
    const store = useBotCreateProgressStore()
    await store.restore({ botId: 'bot-1', botName: 'cat', displayName: 'Cat', setupError: null })
    expect(store.status).toBe('error')
    expect(store.bot).toBeNull()
    expect(store.canRetry).toBe(false)
    expect(store.setupError).toBe('bot not found')
    expect(readCreatedBotSession()).toBeNull()
  })

  it('reset returns the store to idle and drops the retry payload', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([{ type: 'ready', bot }]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })
    store.reset()

    expect(store.status).toBe('idle')
    expect(store.lines).toEqual([])
    expect(store.bot).toBeNull()
    expect(store.setupError).toBeNull()
    expect(store.errorCode).toBeNull()
    expect(store.canRetry).toBe(false)
    expect(readCreatedBotSession()).toBeNull()

    await store.retry()
    expect(postBotsStream).toHaveBeenCalledTimes(1)
  })
})
