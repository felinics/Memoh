// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, nextTick, ref, type App } from 'vue'
const actions = vi.hoisted(() => ({ start: vi.fn(), exchange: vi.fn(), cancel: vi.fn(), handoff: vi.fn(), resume: vi.fn() }))
const session = ref<{ id: string, auth_kind: string, authorization_url?: string } | null>(null)
const ready = ref(false)
const pending = ref(false)
const loading = ref(false)
vi.mock('@/composables/useAgentAuthorization', () => ({ useAgentAuthorization: () => ({
  session, ready, pending, busy: loading, loading, error: ref(''), ...actions,
}) }))
vi.mock('./codex-account-panel.vue', () => ({ default: { template: '<div />' } }))
vi.mock('./agent-credential-input.vue', () => ({ default: { template: '<input data-manual-token />' } }))
vi.mock('@felinic/ui', () => {
  const slot = { template: '<div><slot /></div>' }
  return {
    Button: { props: ['loading'], template: '<button><slot /></button>' },
    Dialog: { props: ['open'], template: '<div v-if="open"><slot /></div>' },
    DialogBody: slot, DialogFooter: slot, DialogHeader: slot, DialogPanel: slot, DialogTitle: slot,
    Input: { props: ['modelValue'], emits: ['update:modelValue'], template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />' },
    Label: { template: '<label><slot /></label>' },
    Select: { props: ['modelValue'], emits: ['update:modelValue'], template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><slot /></select>' },
    SelectContent: { template: '<slot />' }, SelectItem: { props: ['value'], template: '<option :value="value"><slot /></option>' },
    SelectTrigger: { template: '<span />' }, SelectValue: slot, SettingsRow: slot,
  }
})
import AgentAuthorization from './agent-authorization.vue'
let app: App
let root: HTMLDivElement
const status = vi.fn()
beforeEach(() => {
  vi.resetAllMocks()
  session.value = null; ready.value = false; pending.value = false; loading.value = false
  root = document.createElement('div')
  app = createApp(AgentAuthorization, { runtime: 'claude-code', storageKey: 'test', onStatus: status })
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
})
afterEach(() => { app.unmount(); vi.restoreAllMocks() })
function button(label: string) {
  return Array.from(root.querySelectorAll('button')).find(el => el.textContent?.includes(label))!
}

it('offers browser authorization before Bot creation and lets the user open Claude and submit its code', async () => {
  const open = vi.spyOn(window, 'open').mockReturnValue(null)
  button('provider.oauth.connect').click()
  expect(actions.start).toHaveBeenCalledWith('claude_code_oauth')
  session.value = { id: 'authorization', auth_kind: 'claude_code_oauth', authorization_url: 'https://claude.ai/oauth/authorize?state=test' }
  pending.value = true
  await nextTick()
  button('bots.agent.openAuthorizationPage').click()
  expect(open).toHaveBeenCalledWith(session.value.authorization_url, '_blank', 'noopener,noreferrer')
  const input = root.querySelector<HTMLInputElement>('#claude-authorization-code')!
  input.value = 'returned-code#state'; input.dispatchEvent(new Event('input'))
  await nextTick()
  root.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
  expect(actions.exchange).toHaveBeenCalledWith('returned-code#state')
  pending.value = false; ready.value = true
  await nextTick()
  expect(root.querySelector('#claude-authorization-code')).toBeNull()
  expect(status).toHaveBeenLastCalledWith({ ready: true, busy: false, id: 'authorization', auth: 'oauth_token' })
})

it('keeps manual OAuth token entry as a separate choice', async () => {
  expect(root.querySelector('[data-manual-token]')).toBeNull()
  const select = root.querySelector('select')!
  select.value = 'oauth_token'; select.dispatchEvent(new Event('change'))
  await nextTick()
  expect(actions.cancel).toHaveBeenCalled()
  expect(root.querySelector('[data-manual-token]')).not.toBeNull()
  expect(actions.start).not.toHaveBeenCalled()
})
