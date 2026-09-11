// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref, type App } from 'vue'
const api = vi.hoisted(() => ({ create: vi.fn(), get: vi.fn(), poll: vi.fn(), exchange: vi.fn(), cancel: vi.fn() }))
vi.mock('@memohai/sdk', () => ({ postAgentAuthorizations: api.create, getAgentAuthorizationsById: api.get,
  postAgentAuthorizationsByIdPoll: api.poll, postAgentAuthorizationsByIdExchange: api.exchange, deleteAgentAuthorizationsById: api.cancel }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
import { useAgentAuthorization } from './useAgentAuthorization'
let app: App
let state: ReturnType<typeof useAgentAuthorization>
const runtime = ref('codex')
const key = 'authorization-test'
function session(status = 'pending') {
  return { id: 'session', runtime: 'codex', auth_kind: 'openai_codex_oauth', status,
    expires_at: new Date(Date.now() + 60_000).toISOString(), interval_seconds: 5,
    ...(status === 'pending' && { user_code: '123456', verification_url: 'https://auth.openai.com/codex/device' }) }
}
function mount() {
  app = createApp({ setup() { state = useAgentAuthorization(() => runtime.value, key); return () => h('div') } })
  app.mount(document.createElement('div'))
}
beforeEach(() => { vi.useFakeTimers(); vi.resetAllMocks(); sessionStorage.clear(); runtime.value = 'codex'; api.cancel.mockResolvedValue({}); api.create.mockResolvedValue({ data: session() }); api.poll.mockResolvedValue({ data: session('ready') }) })
afterEach(() => { app?.unmount(); vi.useRealTimers() })
it('does not authorize on selection; polls at the provider interval and only persists an opaque reference', async () => {
  mount(); expect(api.create).not.toHaveBeenCalled()
  await state.start('openai_codex_oauth')
  expect(state.ready.value).toBe(false)
  expect(JSON.parse(sessionStorage.getItem(key)!)).toEqual({ id: 'session', runtime: 'codex' })
  await vi.advanceTimersByTimeAsync(4999); expect(api.poll).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(1); expect(state.ready.value).toBe(true)
  await vi.advanceTimersByTimeAsync(60_000); expect(state.ready.value).toBe(false)
  expect(api.cancel).toHaveBeenCalled()
})
it('discards a delayed device-code response after switching Agent', async () => {
  let finish!: (value: unknown) => void
  api.create.mockReturnValue(new Promise(resolve => { finish = resolve }))
  mount(); const pending = state.start('openai_codex_oauth')
  runtime.value = 'claude-code'; await nextTick()
  finish({ data: session() }); await pending
  expect(state.session.value).toBeNull()
  expect(api.cancel).toHaveBeenCalledWith(expect.objectContaining({ path: { id: 'session' } }))
})
it('restores a staged credential from the server and preserves it when creation takes ownership', async () => {
  sessionStorage.setItem(key, JSON.stringify({ id: 'session', runtime: 'codex' }))
  api.get.mockResolvedValue({ data: session('ready') })
  mount(); await Promise.resolve(); await nextTick()
  expect(state.ready.value).toBe(true)
  state.handoff(); app.unmount()
  expect(api.cancel).not.toHaveBeenCalled()
  expect(sessionStorage.getItem(key)).toBeNull()
})
it('clears staged secrets on navigation and never saves an API key in browser storage', async () => {
  runtime.value = 'claude-code'
  api.create.mockResolvedValue({ data: { ...session('ready'), runtime: 'claude-code', auth_kind: 'anthropic_api_key' } })
  mount(); await state.start('anthropic_api_key', 'SECRET-key')
  expect(sessionStorage.getItem(key)).not.toContain('SECRET')
  app.unmount()
  expect(api.cancel).toHaveBeenCalledWith(expect.objectContaining({ path: { id: 'session' } }))
  expect(sessionStorage.getItem(key)).toBeNull()
})

it('resumes expiry and cleanup when Bot creation fails before leaving onboarding', async () => {
  api.create.mockResolvedValue({ data: session('ready') })
  mount(); await state.start('openai_codex_oauth')
  state.handoff(); state.resume()
  expect(sessionStorage.getItem(key)).toContain('session')
  await vi.advanceTimersByTimeAsync(60_000)
  expect(state.ready.value).toBe(false)
  expect(api.cancel).toHaveBeenCalled()
})

it('waits for the Claude code without polling, permits retry, and stores no code or token', async () => {
  runtime.value = 'claude-code'
  const pending = { ...session(), runtime: 'claude-code', auth_kind: 'claude_code_oauth', authorization_url: 'https://claude.ai/oauth/authorize?state=test' }
  api.create.mockResolvedValue({ data: pending })
  api.exchange.mockRejectedValueOnce(new Error('invalid code')).mockResolvedValue({ data: { ...pending, status: 'ready', authorization_url: undefined } })
  mount(); await state.start('claude_code_oauth')
  await vi.advanceTimersByTimeAsync(10_000)
  expect(api.poll).not.toHaveBeenCalled()
  await state.exchange('bad-code')
  expect(state.pending.value).toBe(true)
  expect(state.error.value).toBeTruthy()
  await state.exchange('SECRET-authorization-code')
  expect(state.ready.value).toBe(true)
  expect(state.error.value).toBe('')
  expect(JSON.parse(sessionStorage.getItem(key)!)).toEqual({ id: 'session', runtime: 'claude-code' })
})

it('ignores a late code exchange after cancellation and expires pending Claude sessions', async () => {
  runtime.value = 'claude-code'
  const pending = { ...session(), runtime: 'claude-code', auth_kind: 'claude_code_oauth', authorization_url: 'https://claude.ai/oauth/authorize' }
  api.create.mockResolvedValue({ data: pending })
  let finish!: (value: unknown) => void
  api.exchange.mockReturnValue(new Promise(resolve => { finish = resolve }))
  mount(); await state.start('claude_code_oauth')
  const exchange = state.exchange('code')
  state.cancel()
  finish({ data: { ...pending, status: 'ready' } }); await exchange
  expect(state.session.value).toBeNull()
  await state.start('claude_code_oauth')
  await vi.advanceTimersByTimeAsync(60_000)
  expect(state.session.value).toBeNull()
  expect(state.error.value).toBe('errors.agent_authorization.expired')
})
