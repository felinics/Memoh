// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, type App } from 'vue'
const api = vi.hoisted(() => ({ get: vi.fn(), list: vi.fn(), create: vi.fn(), patch: vi.fn(), defaults: vi.fn(), preflight: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdAgentsById: api.get,
  getBotsByBotIdAgents: api.list,
  postBotsByBotIdAgents: api.create,
  patchBotsByBotIdAgentsById: api.patch,
  putBotsByBotIdSettings: api.defaults,
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@felinic/ui', () => ({
  Button: { template: '<button><slot /></button>' },
  SettingsSection: { template: '<section><slot /></section>' },
  SettingsRow: { props: ['description'], template: '<div>{{ description }}<slot /></div>' },
}))
vi.mock('./dependency-enable-flow.vue', () => ({ default: { setup(_: unknown, { expose }: { expose: (value: unknown) => void }) { expose({ run: api.preflight }); return () => null } } }))
vi.mock('./settings-direct-agent-detail.vue', () => ({ default: { template: '<div />' } }))
vi.mock('./codex-account-login.vue', () => ({ default: {
  emits: ['status'],
  setup(_: unknown, { emit }: { emit: (event: string, state: unknown) => void }) {
    return () => h('button', { 'data-login': '', onClick: () => emit('status', { authorized: true, busy: false }) }, 'Authorize')
  },
} }))
import CreatedAgentSetup from './created-agent-setup.vue'
let app: App
let root: HTMLDivElement
const status = vi.fn()
const agent = { id: 'agent', runtime: 'codex', enabled: false, metadata: { provider: 'codex', auth: 'chatgpt' } }
async function flush() { for (let i = 0; i < 12; i++) await Promise.resolve(); await nextTick() }
function mount(agentId = 'agent') {
  root = document.createElement('div')
  app = createApp(CreatedAgentSetup, { botId: 'bot', agentId, runtime: 'codex', onStatus: status })
  app.mount(root)
}
beforeEach(() => {
  vi.resetAllMocks()
  api.get.mockResolvedValue({ data: agent })
  api.list.mockResolvedValue({ data: { items: [agent] } })
  api.patch.mockResolvedValue({})
  api.defaults.mockResolvedValue({})
  api.preflight.mockResolvedValue(true)
})
afterEach(() => app.unmount())
it('does not start setup without a click, and cancellation cannot enable or authorize', async () => {
  api.preflight.mockResolvedValue(false)
  mount()
  expect(api.get).not.toHaveBeenCalled()
  root.querySelector('button')!.click()
  await flush()
  expect(api.patch).not.toHaveBeenCalled()
  expect(api.defaults).not.toHaveBeenCalled()
  expect(root.querySelector('[data-login]')).toBeNull()
})
it('enables after dependency confirmation, sets the default, then waits for OAuth', async () => {
  mount()
  root.querySelector('button')!.click()
  await flush()
  expect(api.preflight.mock.invocationCallOrder[0]).toBeLessThan(api.patch.mock.invocationCallOrder[0]!)
  expect(api.patch.mock.invocationCallOrder[0]).toBeLessThan(api.defaults.mock.invocationCallOrder[0]!)
  expect(api.defaults).toHaveBeenCalledWith(expect.objectContaining({ body: { default_bot_agent_id: 'agent' } }))
  expect(status).toHaveBeenLastCalledWith({ authorized: false, busy: false })
  root.querySelector<HTMLButtonElement>('[data-login]')!.click()
  await flush()
  expect(status).toHaveBeenLastCalledWith({ authorized: true, busy: false })
})
it('reuses the same Agent on a failed defaults write or a lost creation response', async () => {
  api.defaults.mockRejectedValueOnce(new Error('defaults unavailable'))
  mount('')
  root.querySelector('button')!.click()
  await flush()
  expect(root.textContent).toContain('defaults unavailable')
  expect(root.querySelector('[data-login]')).toBeNull()
  root.querySelector('button')!.click()
  await flush()
  expect(api.create).not.toHaveBeenCalled()
  expect(api.list).toHaveBeenCalledTimes(2)
  expect(root.querySelector('[data-login]')).not.toBeNull()
})
