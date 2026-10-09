// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, ref, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { toast, Toaster } from '@felinic/ui'
import en from '@/i18n/locales/en.json'
import type { AppItem } from '@/composables/api/useApps'
import BotApps from './bot-apps.vue'

const mocks = vi.hoisted(() => ({
  query: vi.fn(),
  reauth: vi.fn(),
  getConnector: vi.fn(),
  invalidate: vi.fn(),
  user: { role: 'member' },
}))

vi.mock('@memohai/sdk', async importOriginal => ({
  ...await importOriginal<typeof import('@memohai/sdk')>(),
  postBotsByBotIdConnectorsByConnectionIdReauth: mocks.reauth,
  getBotsByBotIdConnectorsByConnectionId: mocks.getConnector,
}))
vi.mock('@pinia/colada', () => ({
  useQuery: mocks.query,
  useQueryCache: () => ({ invalidateQueries: mocks.invalidate }),
}))
vi.mock('@/store/user', () => ({ useUserStore: () => ({ userInfo: mocks.user }) }))
vi.mock('@/store/capabilities', () => ({ useCapabilitiesStore: () => ({ connectors: true, load: vi.fn() }) }))
vi.mock('@/lib/api-client', () => ({ sdkApiUrl: vi.fn() }))
vi.mock('../composables/useAppOperation', () => ({ useAppOperation: () => ({ ownsStream: () => false }) }))
vi.mock('../composables/useDependencyOperation', () => ({ useDependencyOperation: () => ({ ownsStream: () => false }) }))
vi.mock('./dependency-confirm-dialog.vue', () => ({ default: { render: () => null } }))
vi.mock('./dependency-progress-dialog.vue', () => ({ default: { render: () => null } }))
vi.mock('./dependency-rollback-dialog.vue', () => ({ default: { render: () => null } }))
vi.mock('./dependency-script-dialog.vue', () => ({ default: { render: () => null } }))
vi.mock('./app-connector-auth-dialog.vue', () => ({ default: { render: () => null } }))
vi.mock('./app-progress-dialog.vue', () => ({ default: { render: () => null } }))
vi.mock('./app-remove-dialog.vue', () => ({ default: { render: () => null } }))
vi.mock('./app-update-dialog.vue', () => ({ default: { render: () => null } }))
vi.mock('./dependency-row.vue', () => ({ default: { render: () => null } }))
vi.mock('@/components/provider-icon/index.vue', () => ({ default: { render: () => null } }))
vi.mock('@/pages/supermarket/components/skill-icon.vue', () => ({ default: { render: () => null } }))

const connector = {
  type: 'github', connection_id: 'connection',
  connector: { connection_id: 'connection', status: 'reauth_required', enabled: true },
}
const item: AppItem = {
  registry_id: 'registry', app_id: 'github-app', installation_id: 'installation',
  name: 'GitHub App', status: 'installed', connectors: [connector],
}
const missingConfiguration = {
  code: 'connector.oauth_client_not_configured', status: 503, fault: 'dependency',
  detail: 'PRIVATE upstream diagnostic',
}

let app: App | undefined
let root: HTMLDivElement

function query(data: unknown) {
  return { data: ref(data), error: ref(null), isLoading: ref(false), refetch: vi.fn() }
}

function popup() {
  return {
    closed: false,
    close: vi.fn(),
    document: document.implementation.createHTMLDocument(),
    location: { href: '' },
  }
}

async function mount(role = 'member') {
  mocks.user.role = role
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { render: () => null } }],
  })
  await router.push({ path: '/', query: { app: 'registry/github-app' } })
  root = document.createElement('div')
  document.body.append(root)
  app = createApp({ render: () => h('div', [h(BotApps, { botId: 'bot' }), h(Toaster)]) })
  app.use(router).use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
}

function reauthorizeButton() {
  const button = Array.from(root.querySelectorAll('button')).find(button => button.textContent?.trim() === en.connectors.reauthorize)
  if (!button) throw new Error('Reauthorize button not found')
  return button
}

function errorNotice() {
  return document.querySelector('[role="status"][data-variant="error"]')
}

beforeEach(() => {
  vi.useFakeTimers()
  mocks.query.mockImplementation(({ key }: { key: () => string[] }) => {
    if (key()[0] === 'bot-apps') return query({ items: [item], workspace_state: 'running' })
    if (key()[0] === 'connectors-catalog') return query([{ type: 'github', name: 'GitHub' }])
    return query({ items: [] })
  })
  mocks.invalidate.mockResolvedValue(undefined)
  mocks.getConnector.mockResolvedValue({ data: { status: 'active' } })
})

afterEach(() => {
  app?.unmount()
  app = undefined
  root?.remove()
  toast.dismiss()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.resetAllMocks()
})

describe('Bot Apps reauthorization entry', () => {
  it.each(['admin', 'member'] as const)('guides a %s through the actual page action', async role => {
    const reserved = popup()
    vi.spyOn(window, 'open').mockReturnValue(reserved as unknown as Window)
    mocks.reauth.mockRejectedValue(missingConfiguration)
    await mount(role)

    reauthorizeButton().click()
    await vi.waitFor(() => expect(errorNotice()?.textContent).toContain(
      en.connectors.oauthAppNotConfigured[role].replaceAll('{connector}', 'GitHub'),
    ))

    expect(mocks.reauth).toHaveBeenCalledExactlyOnceWith({
      path: { bot_id: 'bot', connection_id: 'connection' }, throwOnError: true,
    })
    expect(reserved.close).toHaveBeenCalledOnce()
    expect(reserved.location.href).toBe('')
    expect(mocks.getConnector).not.toHaveBeenCalled()
    expect(mocks.invalidate).not.toHaveBeenCalled()
    expect(errorNotice()?.textContent).not.toContain('PRIVATE')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(errorNotice()).not.toBeNull()
    errorNotice()!.querySelector<HTMLButtonElement>('[aria-label="Dismiss notification"]')!.click()
    await vi.advanceTimersByTimeAsync(100)
    expect(errorNotice()).toBeNull()
  })

  it('clears pending after failure and resumes OAuth after configuration is repaired', async () => {
    const rejectedPopup = popup()
    const retryPopup = popup()
    vi.spyOn(window, 'open')
      .mockReturnValueOnce(rejectedPopup as unknown as Window)
      .mockReturnValueOnce(retryPopup as unknown as Window)
    const rejected = Promise.withResolvers<never>()
    mocks.reauth.mockReturnValueOnce(rejected.promise).mockResolvedValueOnce({ data: { authorization_url: 'https://oauth.example/authorize' } })
    mocks.getConnector.mockResolvedValueOnce({ data: { status: 'pending' } })
    await mount()

    reauthorizeButton().click()
    reauthorizeButton().click()
    expect(mocks.reauth).toHaveBeenCalledOnce()
    expect(window.open).toHaveBeenCalledOnce()
    rejected.reject(missingConfiguration)
    await vi.waitFor(() => expect(errorNotice()).not.toBeNull())
    expect(rejectedPopup.close).toHaveBeenCalledOnce()

    reauthorizeButton().click()
    await vi.waitFor(() => expect(retryPopup.location.href).toBe('https://oauth.example/authorize'))
    expect(mocks.reauth).toHaveBeenCalledTimes(2)
    expect(mocks.getConnector).toHaveBeenCalledWith({ path: { bot_id: 'bot', connection_id: 'connection' }, throwOnError: true })
    expect(retryPopup.close).not.toHaveBeenCalled()
    expect(mocks.invalidate).not.toHaveBeenCalled()
    expect(document.querySelector('[data-variant="success"]')).toBeNull()
    await vi.advanceTimersByTimeAsync(2_000)
    await vi.waitFor(() => expect(document.querySelector('[data-variant="success"]')?.textContent).toContain(en.connectors.oauthSuccess))
    expect(retryPopup.close).toHaveBeenCalledOnce()
    expect(mocks.invalidate).toHaveBeenCalledWith({ key: ['bot-apps', 'bot'] })
    expect(mocks.invalidate).toHaveBeenCalledWith({ key: ['bot-connectors', 'bot'] })
  })
})
