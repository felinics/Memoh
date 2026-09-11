import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { BotCreateStreamEvent } from '@/composables/api/useBotCreateStream'

const postBotsStream = vi.fn()
const postBotsByBotIdAgents = vi.fn()
const claimCredential = vi.fn()
const putBotsByBotIdSettings = vi.fn()
const installAgent = vi.fn()
const patchAgent = vi.fn()
const getAgent = vi.fn()
const getAgents = vi.fn()
const getAuthorization = vi.fn()
vi.mock('./install-created-agent', () => ({ installCreatedAgent: (...args: unknown[]) => installAgent(...args) }))

vi.mock('@/composables/api/useBotCreateStream', async (importActual) => {
  const actual = await importActual<typeof import('@/composables/api/useBotCreateStream')>()
  return { ...actual, postBotsStream: (...args: unknown[]) => postBotsStream(...args) }
})

vi.mock('@memohai/sdk', () => ({
  patchBotsByBotIdAgentsById: (...args: unknown[]) => patchAgent(...args),
  getBotsByBotIdAgentsById: (...args: unknown[]) => getAgent(...args),
  getBotsByBotIdAgents: (...args: unknown[]) => getAgents(...args),
  getAgentAuthorizationsById: (...args: unknown[]) => getAuthorization(...args),
  postBotsByBotIdAgents: (...args: unknown[]) => postBotsByBotIdAgents(...args),
  postBotsByBotIdAgentsByIdCredentialClaim: (...args: unknown[]) => claimCredential(...args),
  putBotsByBotIdSettings: (...args: unknown[]) => putBotsByBotIdSettings(...args),
}))

const { useBotCreateProgressStore } = await import('./bot-create-progress')

function streamOf(events: BotCreateStreamEvent[]) {
  return {
    stream: (async function* () {
      for (const event of events) yield event
    })(),
  }
}

describe('useBotCreateProgressStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    installAgent.mockReset().mockResolvedValue(undefined)
    patchAgent.mockReset().mockResolvedValue({})
    getAgent.mockReset().mockResolvedValue({ data: { id: 'agent-1', runtime: 'codex', enabled: false, agent_credential_id: 'credential-1' } })
    getAgents.mockReset().mockResolvedValue({ data: { items: [] } })
    getAuthorization.mockReset().mockResolvedValue({ data: { auth_kind: 'openai_codex_oauth' } })
    postBotsStream.mockReset()
    postBotsByBotIdAgents.mockReset()
    claimCredential.mockReset()
    claimCredential.mockResolvedValue({ data: { id: 'credential-1' } })
    putBotsByBotIdSettings.mockReset()
    postBotsByBotIdAgents.mockResolvedValue({ data: { id: 'agent-1' } })
    putBotsByBotIdSettings.mockResolvedValue({ data: {} })
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

  it('treats a setup failure after the bot exists as a ready-with-warning state', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'creating' },
      { type: 'error', message: 'container setup failed' },
    ]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('ready')
    expect(store.bot).toEqual(bot)
    expect(store.setupError).toBe('container setup failed')
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
    expect(postBotsByBotIdAgents).toHaveBeenCalledTimes(1)
    expect(claimCredential).toHaveBeenCalledTimes(1)
    expect(store.status).toBe('ready')
    expect(store.lines.at(-1)?.kind).toBe('ready')
  })

  it('resumes the saved Agent instead of creating another Bot after a refresh', async () => {
    const store = useBotCreateProgressStore()
    await store.restore({ botId: 'bot-1', botName: 'cat', displayName: 'Cat', runtime: 'codex', agentId: 'agent-1', authorizationId: 'stage', setupError: null })
    expect(store.status).toBe('ready')
    expect(postBotsStream).not.toHaveBeenCalled()
    expect(postBotsByBotIdAgents).not.toHaveBeenCalled()
    expect(installAgent).toHaveBeenCalled()
    expect(store.createdAgent?.enabled).toBe(true)
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

    await store.retry()
    expect(postBotsStream).toHaveBeenCalledTimes(1)
  })
})
