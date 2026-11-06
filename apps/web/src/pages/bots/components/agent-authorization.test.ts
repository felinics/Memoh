// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, nextTick, ref, type App } from 'vue'
const actions = vi.hoisted(() => ({ start: vi.fn(), cancel: vi.fn(), handoff: vi.fn(), resume: vi.fn() }))
vi.mock('@/composables/useAgentAuthorization', () => ({ useAgentAuthorization: () => ({
  session: ref(null), ready: ref(false), loading: ref(false), error: ref(''), ...actions,
}) }))
vi.mock('./agent-credential-input.vue', () => ({ default: { template: '<input data-api-key />' } }))
vi.mock('@felinic/ui', () => {
  const slot = { template: '<div><slot /></div>' }
  return {
    Select: { props: ['modelValue'], emits: ['update:modelValue'], template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><slot /></select>' },
    SelectContent: { template: '<slot />' }, SelectItem: { props: ['value'], template: '<option :value="value"><slot /></option>' },
    SelectTrigger: { template: '<span />' }, SelectValue: slot, SettingsRow: slot,
  }
})
import AgentAuthorization from './agent-authorization.vue'
let app: App
let root: HTMLDivElement
const status = vi.fn()
function mount(runtime: string) {
  app = createApp(AgentAuthorization, { runtime, storageKey: 'test', onStatus: status })
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
}
beforeEach(() => {
  vi.resetAllMocks()
  root = document.createElement('div')
})
afterEach(() => { app.unmount() })

// The account signs in on the Agent's own process: nothing to stage, nothing
// to wait for before the Bot can be created.
it.each([
  ['claude-code', 'claude_account', 'anthropic_api_key'],
  ['grok-build', 'grok_account', 'xai_api_key'],
])('offers %s an account that signs in after creation by default and an API key as the other choice', async (runtime, account, apiKeyKind) => {
  mount(runtime)
  const select = root.querySelector('select')!
  expect(Array.from(select.options).map(option => option.value)).toEqual([account, 'api_key'])
  expect(root.querySelector('[data-api-key]')).toBeNull()
  expect(status).toHaveBeenLastCalledWith({ ready: true, busy: false, id: '', auth: account })
  select.value = 'api_key'; select.dispatchEvent(new Event('change'))
  await nextTick()
  root.querySelector('[data-api-key]')!.dispatchEvent(new Event('save'))
  expect(actions.start).toHaveBeenLastCalledWith(apiKeyKind, '')
  expect(status).toHaveBeenLastCalledWith({ ready: false, busy: false, id: '', auth: 'api_key' })
})
