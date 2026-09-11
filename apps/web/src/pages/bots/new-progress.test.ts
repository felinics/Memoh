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
vi.mock('@/composables/useOnboarding', () => ({ useOnboarding: () => ({ nextStep, prevStep }) }))
vi.mock('@/store/install-created-agent', () => ({ installCreatedAgent: vi.fn() }))

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

  it('keeps installation failures on the progress page with a retry action', async () => {
    const mounted = await mountKeptProgress()
    mounted.store.status = 'setup-error'
    await nextTick()
    await vi.advanceTimersByTimeAsync(1000)
    expect(routerReplace).not.toHaveBeenCalled()
    expect(mounted.root.querySelector('button')?.textContent).toBe('bots.create.retry')
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
