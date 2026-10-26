// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, ref, type App } from 'vue'
const api = vi.hoisted(() => ({
  authorize: vi.fn(), poll: vi.fn(), cancel: vi.fn(), patch: vi.fn(),
  invalidate: vi.fn(), toastSuccess: vi.fn(), toastError: vi.fn(),
}))
vi.mock('@memohai/sdk', () => ({
  postBotsByBotIdAgentsByIdLoginDeviceAuthorize: api.authorize, postBotsByBotIdAgentsByIdLoginDevicePoll: api.poll,
  postBotsByBotIdAgentsByIdLoginDeviceCancel: api.cancel, patchBotsByBotIdAgentsById: api.patch,
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => true }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
const runtimeAccount = ref<{ signed_in: boolean, email?: string } | undefined>()
vi.mock('@pinia/colada', () => ({
  useQuery: (options: { key: () => string[] }) => ({ data: options.key()[0] === 'agent-account' ? runtimeAccount : ref(undefined), error: ref(null) }),
  useQueryCache: () => ({ invalidateQueries: api.invalidate }),
}))
vi.mock('./device-login-panel.vue', () => ({ default: {
  props: ['auth', 'deviceLogin'],
  template: '<div data-device-login :data-auth="auth" :data-code="deviceLogin?.user_code"><slot /></div>',
} }))
vi.mock('./agent-credential-input.vue', () => ({ default: { template: '<input />' } }))
vi.mock('./agent-credential-disconnect.vue', () => ({ default: { template: '<button />' } }))
vi.mock('./codex-usage-rows.vue', () => ({ default: { template: '<div data-codex-usage />' } }))
vi.mock('@felinic/ui', () => {
  const slot = { template: '<div><slot /></div>' }
  return {
    Input: { template: '<input />' },
    Select: { props: ['modelValue', 'disabled'], emits: ['update:modelValue'], template: '<select :value="modelValue" :disabled="disabled" @change="$emit(\'update:modelValue\', $event.target.value)"><slot /></select>' },
    SelectContent: { template: '<slot />' }, SelectItem: { props: ['value'], template: '<option :value="value"><slot /></option>' },
    SelectTrigger: { template: '<span />' }, SelectValue: slot, SettingsRow: slot, SettingsSection: slot,
    toast: { success: api.toastSuccess, error: api.toastError },
  }
})
import SettingsDirectAgentDetail from './settings-direct-agent-detail.vue'

let app: App | undefined
let root: HTMLDivElement
const agent = ref<Record<string, unknown>>({})
const login = { login_id: 'login-1', user_code: 'ABCD-1234', verification_url: 'https://auth.openai.com/codex/device?user_code=ABCD-1234' }
function panel() { return root.querySelector('[data-device-login]') as HTMLElement | null }
function connect() { panel()!.dispatchEvent(new Event('connect')) }
function mount(runtime: string, auth: string, connected = false) {
  agent.value = { id: 'agent', runtime, metadata: { provider: runtime, auth }, ...(connected && { agent_credential_id: 'cred' }) }
  root = document.createElement('div')
  app = createApp(() => h(SettingsDirectAgentDetail, { botId: 'bot', agent: agent.value }))
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
}
beforeEach(() => {
  vi.useFakeTimers(); vi.resetAllMocks()
  runtimeAccount.value = { signed_in: false }
  api.authorize.mockResolvedValue({ data: login })
  api.poll.mockResolvedValue({ data: { status: 'pending' } })
  api.cancel.mockResolvedValue({}); api.patch.mockResolvedValue({ data: {} }); api.invalidate.mockResolvedValue(undefined)
})
afterEach(() => { app?.unmount(); app = undefined; vi.useRealTimers() })

it('signs a ChatGPT account in on the Agent itself and polls until Codex has stored the login', async () => {
  mount('codex', 'chatgpt')
  expect(panel()!.dataset.auth).toBe('chatgpt')
  connect()
  await vi.advanceTimersByTimeAsync(0)
  expect(api.authorize).toHaveBeenCalledWith(expect.objectContaining({ path: { bot_id: 'bot', id: 'agent' } }))
  expect(panel()!.dataset.code).toBe('ABCD-1234')
  // The mode cannot change under a sign-in that is waiting for the user.
  expect(root.querySelector('select')!.disabled).toBe(true)
  api.poll.mockResolvedValueOnce({ data: { status: 'success' } })
  await vi.advanceTimersByTimeAsync(2000)
  expect(api.poll).toHaveBeenLastCalledWith(expect.objectContaining({ body: { login_id: 'login-1' } }))
  expect(api.invalidate).toHaveBeenCalledWith({ key: ['agent-account', 'bot', 'agent'] })
  expect(api.toastSuccess).toHaveBeenCalledWith('bots.agent.codexLoginSuccess')
  expect(panel()!.dataset.code).toBeUndefined()
})

it('reports a sign-in Codex could not finish with its own copy', async () => {
  mount('codex', 'chatgpt')
  connect()
  await vi.advanceTimersByTimeAsync(0)
  api.poll.mockResolvedValueOnce({ data: { status: 'error' } })
  await vi.advanceTimersByTimeAsync(2000)
  expect(api.toastError).toHaveBeenCalledWith('bots.agent.codexLoginFailed')
  expect(root.querySelector('select')!.disabled).toBe(false)
})

it('cancels a pending sign-in through the Agent', async () => {
  mount('codex', 'chatgpt')
  connect()
  await vi.advanceTimersByTimeAsync(0)
  panel()!.dispatchEvent(new Event('cancel'))
  await vi.advanceTimersByTimeAsync(0)
  expect(api.cancel).toHaveBeenCalledWith(expect.objectContaining({ body: { login_id: 'login-1' } }))
  const polls = api.poll.mock.calls.length
  await vi.advanceTimersByTimeAsync(4000)
  expect(api.poll).toHaveBeenCalledTimes(polls)
})

it('signs a Grok account in through the same flow with Grok Build\'s own copy', async () => {
  mount('grok-build', 'grok_account')
  expect(panel()!.dataset.auth).toBe('grok_account')
  connect()
  await vi.advanceTimersByTimeAsync(0)
  api.poll.mockResolvedValueOnce({ data: { status: 'success' } })
  await vi.advanceTimersByTimeAsync(2000)
  expect(api.toastSuccess).toHaveBeenCalledWith('bots.agent.grokLoginSuccess')
})

it('takes the ChatGPT sign-in from Codex itself, not from a stored credential', async () => {
  mount('codex', 'chatgpt', true)
  expect(root.querySelector('[data-codex-usage]')).toBeNull()
  runtimeAccount.value = { signed_in: true, email: 'user@example.com' }
  await vi.advanceTimersByTimeAsync(0)
  expect(panel()!.dataset.auth).toBe('chatgpt')
  expect(root.querySelector('[data-codex-usage]')).not.toBeNull()
})
