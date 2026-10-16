// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { createApp, h, KeepAlive, nextTick, ref } from 'vue'
import type { Slots } from 'vue'
import { useBotCreateProgressStore } from '@/store/bot-create-progress'

const routerReplace = vi.fn()
const invalidateQueries = vi.fn()

function translate(key: string, params?: Record<string, string>) {
  return params?.name ? `${key}:${params.name}` : key
}

vi.mock('vue-router', () => ({
  useRouter: () => ({
    replace: routerReplace,
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: translate,
  }),
}))

vi.mock('vue-sonner', () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}))

vi.mock('@pinia/colada', () => ({
  useQueryCache: () => ({
    invalidateQueries,
  }),
}))

vi.mock('@memohai/sdk/colada', () => ({
  getBotsQueryKey: () => ['bots'],
}))

vi.mock('@felinic/ui', async () => {
  const { h } = await import('vue')
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => {
    return h('div', slots.default?.())
  }
  const Button = Object.assign((
    _props: Record<string, unknown>,
    { emit, slots }: { emit: (event: 'click') => void, slots: Slots },
  ) => {
    return h('button', { ..._props, onClick: () => emit('click') }, slots.default?.())
  }, {
    emits: ['click'],
  })
  return {
    Avatar: Passthrough,
    AvatarFallback: Passthrough,
    AvatarImage: Passthrough,
    Button,
    Spinner: Passthrough,
    toast: {
      error: () => {},
      success: () => {},
    },
  }
})

const nextStep = vi.fn()
const prevStep = vi.fn()
const getBot = vi.fn()
const getBotChecks = vi.fn()
const putSettings = vi.fn()
vi.mock('@/composables/useOnboarding', () => ({ useOnboarding: () => ({ nextStep, prevStep }) }))
vi.mock('@/store/install-created-agent', () => ({ installCreatedAgent: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  getBotsById: (...args: unknown[]) => getBot(...args),
  getBotsByIdChecks: (...args: unknown[]) => getBotChecks(...args),
  putBotsByBotIdSettings: (...args: unknown[]) => putSettings(...args),
  getAgentAuthorizationsById: vi.fn(),
  getBotsByBotIdAgents: vi.fn(),
  getBotsByBotIdAgentsById: vi.fn(),
  patchBotsByBotIdAgentsById: vi.fn(),
  postBotsByBotIdAgents: vi.fn(),
  postBotsByBotIdAgentsByIdCredentialClaim: vi.fn(),
  postBotsByBotIdUserAccess: vi.fn(),
}))
vi.mock('@/composables/api/useContainerStream', () => ({ postBotsByBotIdContainerStream: vi.fn() }))

function setupStore() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const store = useBotCreateProgressStore()
  store.status = 'creating'
  store.display = { display_name: 'Prog', name: 'prog' }
  store.bot = { id: 'bot-1', name: 'prog' }
  return { pinia, store }
}

async function mountKeptProgress(onboarding = false) {
  const { pinia, store } = setupStore()
  const ProgressPage = (await import('./new-progress.vue')).default
  const show = ref(true)
  const app = createApp({
    name: 'ProgressRouteTestHost',
    setup() {
      return () => h(KeepAlive, null, () => show.value ? h(ProgressPage, { onboarding }) : h('div'))
    },
  })
  const root = document.createElement('div')
  document.body.append(root)
  app.use(pinia)
  app.config.globalProperties.$t = translate
  app.mount(root)
  await nextTick()

  return {
    app,
    root,
    show,
    store,
    async deactivate() {
      show.value = false
      await nextTick()
    },
    async activate() {
      show.value = true
      await nextTick()
    },
  }
}

async function mountIdleProgress(onboarding = false) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const store = useBotCreateProgressStore()
  const ProgressPage = (await import('./new-progress.vue')).default
  const app = createApp({
    name: 'ProgressRefreshTestHost',
    setup() {
      return () => h(ProgressPage, { onboarding })
    },
  })
  const root = document.createElement('div')
  document.body.append(root)
  app.use(pinia)
  app.config.globalProperties.$t = translate
  app.mount(root)
  await nextTick()
  return { app, root, store }
}

function buttonLabels(root: HTMLElement) {
  return Array.from(root.querySelectorAll('button')).map(button => button.textContent?.trim())
}

describe('bot create progress route', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    routerReplace.mockReset()
    nextStep.mockReset()
    prevStep.mockReset()
    sessionStorage.clear()
    // Real vue-router returns a Promise that resolves when navigation commits.
    routerReplace.mockResolvedValue(undefined)
    invalidateQueries.mockReset()
    getBot.mockReset().mockResolvedValue({ data: { id: 'bot-1', name: 'prog', display_name: 'Prog', status: 'ready' } })
    getBotChecks.mockReset().mockResolvedValue({ data: { items: [] } })
    putSettings.mockReset().mockResolvedValue({ data: {} })
    document.body.innerHTML = ''
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('does not navigate after the ready redirect delay when deactivated', async () => {
    const mounted = await mountKeptProgress()

    mounted.store.status = 'ready'
    await nextTick()
    await mounted.deactivate()
    await vi.advanceTimersByTimeAsync(700)

    expect(routerReplace).not.toHaveBeenCalled()
    expect(invalidateQueries).not.toHaveBeenCalled()

    mounted.app.unmount()
    mounted.root.remove()
  })

  it('keeps the terminal intact until navigation commits, then resets', async () => {
    let resolveReplace: (() => void) | undefined
    routerReplace.mockImplementation(() => new Promise<void>((resolve) => {
      resolveReplace = resolve
    }))

    const mounted = await mountKeptProgress()
    mounted.store.status = 'ready'
    mounted.store.lines = [{ id: 'ready', kind: 'ready', status: 'done' }]
    await nextTick()

    // Fire the 700ms ready redirect: goToBot() -> router.replace() (still pending).
    await vi.advanceTimersByTimeAsync(700)

    expect(routerReplace).toHaveBeenCalledWith({ name: 'bot-detail', params: { botName: 'prog' } })
    // Navigation has not committed yet, so the store must stay intact — otherwise
    // the still-visible terminal flashes empty before the view swaps.
    expect(mounted.store.status).toBe('ready')
    expect(mounted.store.lines).toHaveLength(1)

    // Commit the navigation; only now should the store reset.
    resolveReplace?.()
    await nextTick()
    await nextTick()

    expect(mounted.store.status).toBe('idle')
    expect(mounted.store.lines).toEqual([])

    mounted.app.unmount()
    mounted.root.remove()
  })

  it('shows only the terminal while installing, then opens the Bot automatically', async () => {
    const mounted = await mountKeptProgress()
    mounted.store.createdAgent = { id: 'agent-1', runtime: 'codex' }
    mounted.store.lines = [{ id: 'install', kind: 'installing-agent', status: 'running', message: 'Codex' }]
    await nextTick()
    await vi.advanceTimersByTimeAsync(1000)
    expect(routerReplace).not.toHaveBeenCalled()
    expect(mounted.root.textContent).toContain('bots.create.line.installingAgent:Codex')
    expect(mounted.root.querySelectorAll('button, [role="dialog"], input')).toHaveLength(0)
    mounted.store.status = 'ready'
    await nextTick()
    await vi.advanceTimersByTimeAsync(700)
    expect(routerReplace).toHaveBeenCalledWith({ name: 'bot-detail', params: { botName: 'prog' } })
    mounted.app.unmount(); mounted.root.remove()
  })

  it('advances the dedicated onboarding progress step only after setup is ready', async () => {
    const mounted = await mountKeptProgress(true)
    mounted.store.createdAgent = { id: 'agent-1', runtime: 'claude-code', enabled: true }
    mounted.store.lines = [{ id: 'install', kind: 'installing-agent', status: 'running', message: 'Claude Code' }]
    await nextTick()
    await vi.advanceTimersByTimeAsync(1000)
    expect(nextStep).not.toHaveBeenCalled()
    expect(mounted.root.querySelectorAll('button, [role="dialog"], input')).toHaveLength(0)
    mounted.store.status = 'ready'
    await nextTick()
    await vi.advanceTimersByTimeAsync(700)
    expect(nextStep).toHaveBeenCalledTimes(1)
    expect(routerReplace).not.toHaveBeenCalled()
    mounted.app.unmount(); mounted.root.remove()
  })

  it('keeps installation failures on the progress page with a retry and a way out', async () => {
    const mounted = await mountKeptProgress()
    mounted.store.status = 'setup-error'
    await nextTick()
    await vi.advanceTimersByTimeAsync(1000)
    expect(routerReplace).not.toHaveBeenCalled()
    expect(buttonLabels(mounted.root)).toEqual(['bots.create.continueLater', 'bots.create.retry'])
    mounted.app.unmount(); mounted.root.remove()
  })

  it('offers a workspace retry and leaves the failed Bot behind on continue later', async () => {
    const mounted = await mountKeptProgress()
    mounted.store.status = 'workspace-error'
    mounted.store.setupError = 'workspace setup failed'
    await nextTick()
    await vi.advanceTimersByTimeAsync(1000)
    expect(routerReplace).not.toHaveBeenCalled()
    expect(buttonLabels(mounted.root)).toEqual(['bots.create.continueLater', 'bots.create.retryWorkspace'])

    mounted.root.querySelector('button')!.click()
    await nextTick()
    await nextTick()
    // The Bot keeps its failed workspace; its detail page owns the next attempt.
    expect(routerReplace).toHaveBeenCalledWith({ name: 'bot-detail', params: { botName: 'prog' } })
    expect(invalidateQueries).toHaveBeenCalled()
    mounted.app.unmount(); mounted.root.remove()
  })

  it('lets onboarding continue past a failed workspace with the created Bot', async () => {
    const mounted = await mountKeptProgress(true)
    mounted.store.status = 'workspace-error'
    await nextTick()
    expect(buttonLabels(mounted.root)).toEqual(['bots.create.continueLater', 'bots.create.retryWorkspace'])

    mounted.root.querySelector('button')!.click()
    await nextTick()
    expect(nextStep).toHaveBeenCalledTimes(1)
    expect(routerReplace).not.toHaveBeenCalled()
    expect(JSON.parse(sessionStorage.getItem('memoh:onboarding:bot-result') ?? 'null')).toMatchObject({ botId: 'bot-1', modelConfigured: false })
    expect(mounted.store.status).toBe('idle')
    mounted.app.unmount(); mounted.root.remove()
  })

  it('opens the Bot that already owns a taken name instead of stopping at the error', async () => {
    const mounted = await mountKeptProgress()
    mounted.store.bot = null
    mounted.store.status = 'error'
    mounted.store.errorCode = 'bot.name_taken'
    await nextTick()
    expect(buttonLabels(mounted.root)).toEqual(['bots.create.back', 'bots.create.openExisting'])

    mounted.root.querySelectorAll('button')[1]!.click()
    await nextTick()
    await nextTick()
    expect(routerReplace).toHaveBeenCalledWith({ name: 'bot-detail', params: { botName: 'prog' } })
    expect(mounted.store.status).toBe('idle')
    mounted.app.unmount(); mounted.root.remove()
  })

  it('hides the retry when a resumed Bot turned out to be gone', async () => {
    const mounted = await mountKeptProgress()
    mounted.store.bot = null
    mounted.store.status = 'error'
    mounted.store.errorCode = null
    await nextTick()
    expect(buttonLabels(mounted.root)).toEqual(['bots.create.back'])
    mounted.app.unmount(); mounted.root.remove()
  })

  it('resumes the persisted Bot after a refresh instead of returning to the form', async () => {
    sessionStorage.setItem('memoh:new-bot:authorization', JSON.stringify({ botId: 'bot-1', botName: 'prog', displayName: 'Prog', setupError: null, settings: { chat_model_id: 'm1' } }))
    const mounted = await mountIdleProgress()
    expect(routerReplace).not.toHaveBeenCalledWith({ name: 'bot-new' })
    expect(getBot).toHaveBeenCalledWith(expect.objectContaining({ path: { id: 'bot-1' } }))
    await vi.advanceTimersByTimeAsync(0)
    expect(putSettings).toHaveBeenCalledWith(expect.objectContaining({ path: { bot_id: 'bot-1' }, body: { chat_model_id: 'm1' } }))
    expect(mounted.store.status).toBe('ready')
    await vi.advanceTimersByTimeAsync(700)
    expect(routerReplace).toHaveBeenCalledWith({ name: 'bot-detail', params: { botName: 'prog' } })
    mounted.app.unmount(); mounted.root.remove()
  })

  it('shows the failed workspace after an onboarding refresh without creating again', async () => {
    sessionStorage.setItem('memoh:onboarding:creation', JSON.stringify({ botId: 'bot-1', botName: 'prog', displayName: 'Prog', setupError: null }))
    getBot.mockResolvedValue({ data: { id: 'bot-1', name: 'prog', display_name: 'Prog', status: 'failed' } })
    getBotChecks.mockResolvedValue({ data: { items: [{ type: 'container.init', status: 'error', detail: 'image pull failed' }] } })
    const mounted = await mountIdleProgress(true)
    await vi.advanceTimersByTimeAsync(0)
    expect(prevStep).not.toHaveBeenCalled()
    expect(mounted.store.status).toBe('workspace-error')
    expect(mounted.root.textContent).toContain('image pull failed')
    expect(buttonLabels(mounted.root)).toEqual(['bots.create.continueLater', 'bots.create.retryWorkspace'])
    mounted.app.unmount(); mounted.root.remove()
  })

  it('redirects back to the create form when reactivated without an in-memory stream', async () => {
    const mounted = await mountKeptProgress()

    await mounted.deactivate()
    mounted.store.reset()
    routerReplace.mockClear()

    await mounted.activate()

    expect(routerReplace).toHaveBeenCalledWith({ name: 'bot-new' })

    mounted.app.unmount()
    mounted.root.remove()
  })
})
