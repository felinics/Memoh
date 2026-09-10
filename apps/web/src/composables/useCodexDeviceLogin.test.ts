// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, ref, type App } from 'vue'

const api = vi.hoisted(() => ({ status: vi.fn(), authorize: vi.fn(), poll: vi.fn(), cancel: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdAgentsByIdCredential: api.status,
  postBotsByBotIdAgentsByIdCodexLoginDeviceAuthorize: api.authorize,
  postBotsByBotIdAgentsByIdCodexLoginDevicePoll: api.poll,
  postBotsByBotIdAgentsByIdCodexLoginDeviceCancel: api.cancel,
}))
import { useCodexDeviceLogin } from './useCodexDeviceLogin'

let app: App
let host: HTMLDivElement
let login: ReturnType<typeof useCodexDeviceLogin>
const agentId = ref('agent-1')
const session = { login_id: 'login-1', user_code: 'TEST-CODE', verification_url: 'https://example.com/device' }
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.resetAllMocks()
  agentId.value = 'agent-1'
  api.authorize.mockResolvedValue({ data: session })
  api.poll.mockResolvedValue({ data: { status: 'pending' } })
  api.cancel.mockResolvedValue({})
  api.status.mockResolvedValue({ data: { auth_kind: 'openai_codex_oauth', revoked: false } })
  host = document.createElement('div')
  app = createApp({ setup() {
    login = useCodexDeviceLogin(() => 'bot-1', () => agentId.value)
    return () => h('div')
  } })
  app.mount(host)
})
afterEach(() => { app.unmount(); vi.useRealTimers() })

it('does not authorize on mount and only accepts a non-revoked Codex credential', async () => {
  expect(api.authorize).not.toHaveBeenCalled()
  expect(await login.loadStatus()).toBe(true)
  api.status.mockResolvedValue({ data: { auth_kind: 'openai_codex_oauth', revoked: true } })
  expect(await login.loadStatus()).toBe(false)
  api.status.mockResolvedValue({ data: { auth_kind: 'openai_api_key', revoked: false } })
  expect(await login.loadStatus()).toBe(false)
})

it('polls pending sessions and stops only at an authorization terminal state', async () => {
  await login.authorizeCodex()
  expect(login.busy.value).toBe(true)
  await vi.advanceTimersByTimeAsync(2000)
  expect(login.authorized.value).toBe(false)
  api.poll.mockResolvedValue({ data: { status: 'success' } })
  await vi.advanceTimersByTimeAsync(2000)
  expect(login.authorized.value).toBe(true)
  expect(login.busy.value).toBe(false)
  await vi.advanceTimersByTimeAsync(10000)
  expect(api.poll).toHaveBeenCalledTimes(2)
})

it('ignores a stale successful poll after cancellation', async () => {
  const response = deferred<{ data: { status: string } }>()
  api.poll.mockReturnValue(response.promise)
  await login.authorizeCodex()
  await vi.advanceTimersByTimeAsync(2000)
  await login.cancelCodex()
  response.resolve({ data: { status: 'success' } })
  await Promise.resolve()
  expect(login.authorized.value).toBe(false)
  expect(login.deviceLogin.value).toBeNull()
  expect(api.cancel).toHaveBeenCalledWith(expect.objectContaining({ path: { bot_id: 'bot-1', id: 'agent-1' }, body: { login_id: 'login-1' } }))
})

it('retires an authorization response that arrives after unmount', async () => {
  const response = deferred<{ data: typeof session }>()
  api.authorize.mockReturnValue(response.promise)
  const pending = login.authorizeCodex()
  app.unmount()
  response.resolve({ data: session })
  expect(await pending).toBe(false)
  expect(api.cancel).toHaveBeenCalledTimes(1)
  expect(api.poll).not.toHaveBeenCalled()
})

it('cancels the previous target and rejects its late credential status', async () => {
  await login.authorizeCodex()
  const response = deferred<{ data: { auth_kind: string, revoked: boolean } }>()
  api.status.mockReturnValue(response.promise)
  const pending = login.loadStatus()
  agentId.value = 'agent-2'
  response.resolve({ data: { auth_kind: 'openai_codex_oauth', revoked: false } })
  expect(await pending).toBe(false)
  expect(login.authorized.value).toBe(false)
  expect(api.cancel).toHaveBeenCalledWith(expect.objectContaining({ path: { bot_id: 'bot-1', id: 'agent-1' } }))
})

it('allows retry after a runtime error without changing the Bot or Agent', async () => {
  api.authorize.mockRejectedValueOnce(new Error('runtime unavailable'))
  expect(await login.authorizeCodex()).toBe(false)
  expect(login.busy.value).toBe(false)
  expect(login.error.value).toContain('runtime unavailable')
  expect(await login.authorizeCodex()).toBe(true)
  expect(api.authorize.mock.calls.map(([request]) => request.path)).toEqual([
    { bot_id: 'bot-1', id: 'agent-1' }, { bot_id: 'bot-1', id: 'agent-1' },
  ])
})

it('retries by retiring the prior device login', async () => {
  await login.authorizeCodex()
  api.authorize.mockResolvedValue({ data: { ...session, login_id: 'login-2' } })
  await login.authorizeCodex()
  expect(api.cancel).toHaveBeenCalledTimes(1)
  expect(login.deviceLogin.value?.login_id).toBe('login-2')
})
