// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref, type App } from 'vue'
const api = vi.hoisted(() => ({ create: vi.fn(), get: vi.fn(), cancel: vi.fn() }))
vi.mock('@memohai/sdk', () => ({ postAgentAuthorizations: api.create, getAgentAuthorizationsById: api.get,
  deleteAgentAuthorizationsById: api.cancel }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
import { useAgentAuthorization } from './useAgentAuthorization'
import type { DirectBotAgentRuntime } from '@/utils/bot-agent'
let app: App
let state: ReturnType<typeof useAgentAuthorization>
const runtime = ref<DirectBotAgentRuntime>('codex')
const key = 'authorization-test'
function session(status = 'ready') {
  return { id: 'session', runtime: 'codex', auth_kind: 'openai_api_key', status,
    expires_at: new Date(Date.now() + 60_000).toISOString() }
}
function mount() {
  app = createApp({ setup() { state = useAgentAuthorization(() => runtime.value, key); return () => h('div') } })
  app.mount(document.createElement('div'))
}
beforeEach(() => { vi.useFakeTimers(); vi.resetAllMocks(); sessionStorage.clear(); runtime.value = 'codex'; api.cancel.mockResolvedValue({}); api.create.mockResolvedValue({ data: session() }) })
afterEach(() => { app?.unmount(); vi.useRealTimers() })
it('does not authorize on selection; only persists an opaque reference and expires it', async () => {
  mount(); expect(api.create).not.toHaveBeenCalled()
  await state.start('openai_api_key', 'SECRET-key')
  expect(state.ready.value).toBe(true)
  expect(JSON.parse(sessionStorage.getItem(key)!)).toEqual({ id: 'session', runtime: 'codex' })
  await vi.advanceTimersByTimeAsync(60_000); expect(state.ready.value).toBe(false)
  expect(api.cancel).toHaveBeenCalled()
})
it('discards a delayed response after switching Agent', async () => {
  let finish!: (value: unknown) => void
  api.create.mockReturnValue(new Promise(resolve => { finish = resolve }))
  mount(); const pending = state.start('openai_api_key', 'SECRET-key')
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
  mount(); await state.start('openai_api_key', 'SECRET-key')
  state.handoff(); state.resume()
  expect(sessionStorage.getItem(key)).toContain('session')
  await vi.advanceTimersByTimeAsync(60_000)
  expect(state.ready.value).toBe(false)
  expect(api.cancel).toHaveBeenCalled()
})
