// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, type App } from 'vue'
import AppConnectorAuthForm from './app-connector-auth-form.vue'

const mocks = vi.hoisted(() => ({ begin: vi.fn(), credential: vi.fn(), prepare: vi.fn(), open: vi.fn(), wait: vi.fn() }))
vi.mock('@/composables/api/useApps', () => ({ beginAppConnectorOAuth: mocks.begin, createAppConnectorCredential: mocks.credential }))
vi.mock('@/composables/useConnectorOAuth', () => ({
  prepareConnectorOAuthPopup: mocks.prepare, openConnectorOAuthURL: mocks.open, waitForConnectorOAuth: mocks.wait,
  connectorOAuthErrorKey: () => null,
  isConnectorOAuthCancelled: (error: Error) => error.message === 'cancelled',
}))
vi.mock('@/utils/api-error', () => ({ resolveApiErrorMessage: (_: unknown, fallback: string) => fallback }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

vi.mock('@felinic/ui', async () => {
  const { Field } = await import('vee-validate')
  const slot = { template: '<div><slot /></div>' }
  return {
    Alert: slot, AlertTitle: slot, AlertDescription: slot, FormStack: slot, FormControl: slot,
    FormField: { components: { Field }, props: ['name'], template: '<Field :name="name" v-slot="{ componentField, errors }"><slot :componentField="componentField" /><span>{{ errors[0] }}</span></Field>' },
    FieldStack: { props: ['label'], template: '<div><label>{{ label }}</label><slot /></div>' },
    Input: { props: ['modelValue'], emits: ['update:modelValue'], template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />' },
    Select: { props: ['modelValue'], emits: ['update:modelValue'], template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><slot /></select>' },
    SelectContent: { template: '<slot />' }, SelectItem: { props: ['value'], template: '<option :value="value"><slot /></option>' },
    SelectTrigger: { template: '<span />' }, SelectValue: slot,
  }
})

let app: App | undefined
let root: HTMLDivElement
const flush = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
function setup(methods: unknown[], extra: Record<string, unknown> = {}) {
  const authorized = vi.fn()
  const phase = vi.fn()
  root = document.createElement('div')
  document.body.append(root)
  let form!: InstanceType<typeof AppConnectorAuthForm>
  app = createApp({ render: () => h(AppConnectorAuthForm, {
    ref: (instance: unknown) => { form = instance as typeof form }, botId: 'bot', installationId: 'installation',
    connector: { type: 'example' }, catalog: { type: 'example', auth_methods: methods },
    onAuthorized: authorized, onPhase: phase, ...extra,
  } as never) })
  app.mount(root)
  return { get form() { return form }, authorized, phase }
}
const oauth = [{ key: 'oauth', type: 'oauth2' }]
beforeEach(() => {
  mocks.begin.mockResolvedValue({ connection_id: 'connection', authorization_url: 'https://example.com/authorize' })
  mocks.open.mockResolvedValue(undefined)
})
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks() })

describe('App connector authorization form', () => {
  it('automatically uses the window reserved by Install and waits for active authorization', async () => {
    let complete!: () => void
    mocks.wait.mockReturnValue(new Promise<void>(resolve => { complete = resolve }))
    const popup = { close: vi.fn() }
    const flow = setup(oauth, { autoStart: true, oauthPopup: popup })
    await vi.waitFor(() => expect(mocks.begin).toHaveBeenCalled())
    await flush()
    expect(mocks.prepare).not.toHaveBeenCalled()
    expect(mocks.begin).toHaveBeenCalledWith('bot', 'installation', 'example', 'oauth')
    expect(mocks.open).toHaveBeenCalledWith('https://example.com/authorize', popup)
    expect(flow.authorized).not.toHaveBeenCalled()
    expect(flow.form.phase).toBe('awaiting-oauth')
    complete()
    await flush()
    expect(flow.authorized).toHaveBeenCalledOnce()
  })

  it('offers an explicit Connect action when no popup was reserved', async () => {
    const flow = setup(oauth, { autoStart: true })
    await flush()
    expect(mocks.begin).not.toHaveBeenCalled()
    mocks.prepare.mockReturnValue(null)
    await flow.form.connect()
    expect(root.textContent).toContain('connectors.oauthPopupBlocked')
    expect(mocks.begin).not.toHaveBeenCalled()
  })

  it('cancels pending OAuth and can retry without re-installing', async () => {
    mocks.wait.mockImplementation((_bot, _connection, _popup, signal: AbortSignal) => new Promise((_, reject) => {
      signal.addEventListener('abort', () => reject(new Error('cancelled')))
    }))
    mocks.prepare.mockReturnValue({ close: vi.fn() })
    const flow = setup(oauth)
    const attempt = flow.form.connect()
    await vi.waitFor(() => expect(flow.form.phase).toBe('awaiting-oauth'))
    flow.form.cancel()
    await attempt
    expect(flow.form.phase).toBe('idle')
    expect(flow.authorized).not.toHaveBeenCalled()
    mocks.wait.mockResolvedValueOnce(undefined)
    await flow.form.connect()
    expect(flow.authorized).toHaveBeenCalledOnce()
  })

  it('validates required credential fields before sending API keys', async () => {
    const flow = setup([{ key: 'token', type: 'api_key', credential_fields: [
      { key: 'token', label: 'API key', required: true, secret: true },
    ] }], { autoStart: true })
    await flush()
    expect(mocks.credential).not.toHaveBeenCalled()
    await flow.form.connect()
    expect(mocks.credential).not.toHaveBeenCalled()
    expect(root.textContent).toContain('connectors.validation.fieldRequired')
    const input = root.querySelector('input')!
    expect(input.type).toBe('password')
    input.value = 'test-only-placeholder'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()
    await flow.form.connect()
    expect(mocks.credential).toHaveBeenCalledWith('bot', 'installation', 'example', 'token', { token: 'test-only-placeholder' })
    expect(flow.authorized).toHaveBeenCalledOnce()
  })

  it('does not report authorization after the form is unmounted', async () => {
    let finish!: (result: unknown) => void
    mocks.credential.mockReturnValue(new Promise(resolve => { finish = resolve }))
    const flow = setup([{ key: 'token', type: 'api_key', credential_fields: [] }])
    const attempt = flow.form.connect()
    await vi.waitFor(() => expect(mocks.credential).toHaveBeenCalled())
    app?.unmount(); app = undefined
    finish({})
    await attempt
    expect(flow.authorized).not.toHaveBeenCalled()
  })
})
