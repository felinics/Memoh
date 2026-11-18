// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, ref, type App } from 'vue'
const api = vi.hoisted(() => ({
  authorize: vi.fn(), poll: vi.fn(), cancel: vi.fn(), patch: vi.fn(),
  invalidate: vi.fn(), toastSuccess: vi.fn(), toastError: vi.fn(),
  push: vi.fn(), routeParams: { botName: 'neko' as string | undefined },
}))
vi.mock('@memohai/sdk', () => ({
  postBotsByBotIdAgentsByIdLoginDeviceAuthorize: api.authorize, postBotsByBotIdAgentsByIdLoginDevicePoll: api.poll,
  postBotsByBotIdAgentsByIdLoginDeviceCancel: api.cancel, patchBotsByBotIdAgentsById: api.patch,
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => true }) }))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: api.push }),
  useRoute: () => ({ params: api.routeParams }),
}))
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
vi.mock('./agent-quota-rows.vue', () => ({ default: { template: '<div data-agent-quota />' } }))
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
function mount(runtime: string, auth: string, connected = false, extra: Record<string, unknown> = {}) {
  agent.value = { id: 'agent', runtime, metadata: { provider: runtime, auth }, ...(connected && { agent_credential_id: 'cred' }), ...extra }
  root = document.createElement('div')
  app = createApp(() => h(SettingsDirectAgentDetail, { botId: 'bot', agent: agent.value }))
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
}
beforeEach(() => {
  vi.useFakeTimers(); vi.resetAllMocks()
  api.push.mockResolvedValue(undefined)
  api.routeParams.botName = 'neko'
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

it('takes the ChatGPT sign-in from Codex itself, not from a stored credential', async () => {
  mount('codex', 'chatgpt', true, { reports_quota: true })
  expect(root.querySelector('[data-agent-quota]')).toBeNull()
  runtimeAccount.value = { signed_in: true, email: 'user@example.com' }
  await vi.advanceTimersByTimeAsync(0)
  expect(panel()!.dataset.auth).toBe('chatgpt')
  expect(root.querySelector('[data-agent-quota]')).not.toBeNull()
})

it('routes missing dependency toast action to the apps tab using route botName slug', async () => {
  api.authorize.mockRejectedValueOnce({ error: { code: 'agent_dependency_missing' } })
  mount('codex', 'chatgpt', false, { dependency: { dependency_id: 'codex' } })
  connect()
  await vi.advanceTimersByTimeAsync(0)
  expect(api.toastError).toHaveBeenCalledOnce()

  const options = api.toastError.mock.calls[0]![1] as { action: { onClick: () => void } }
  options.action.onClick()
  expect(api.push).toHaveBeenCalledWith({
    name: 'bot-detail',
    params: { botName: 'neko' },
    query: { tab: 'apps', dependency_id: 'codex' },
  })
})

it('falls back to botId when route botName param is empty', async () => {
  api.routeParams.botName = '   '
  api.authorize.mockRejectedValueOnce({ error: { code: 'agent_dependency_missing' } })
  mount('codex', 'chatgpt', false, { dependency: { dependency_id: 'codex' } })
  connect()
  await vi.advanceTimersByTimeAsync(0)
  expect(api.toastError).toHaveBeenCalledOnce()

  const options = api.toastError.mock.calls[0]![1] as { action: { onClick: () => void } }
  options.action.onClick()
  expect(api.push).toHaveBeenCalledWith({
    name: 'bot-detail',
    params: { botName: 'bot' },
    query: { tab: 'apps', dependency_id: 'codex' },
  })
})
