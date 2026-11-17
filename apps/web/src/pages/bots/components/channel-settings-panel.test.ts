// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import type { ChannelUpsertConfigRequest, HandlersChannelMeta } from '@memohai/sdk'
import ChannelSettingsPanel, { type BotChannelItem } from './channel-settings-panel.vue'

const sdk = vi.hoisted(() => ({
  putBotsByIdChannelByPlatform: vi.fn(),
  deleteBotsByIdChannelByPlatform: vi.fn(),
  patchBotsByIdChannelByPlatformStatus: vi.fn(),
  postBotsByIdChannelByPlatformWebhookEndpoint: vi.fn(),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (k: string) => k }) }))
vi.mock('@pinia/colada', () => ({ useMutation: <A, R>(o: { mutation: (a: A) => Promise<R> }) => ({ mutateAsync: (a: A) => o.mutation(a) }) }))
vi.mock('@memohai/sdk', () => sdk)
vi.mock('@memohai/sdk/client', () => ({ client: { getConfig: () => ({ baseUrl: 'http://127.0.0.1:19380' }) } }))
vi.mock('../composables/use-line-webhook-public-base', () => ({ useLineWebhookPublicBase: () => ({ publicBase: ref({ url: '' }), warningKey: ref('') }) }))
vi.mock('@/components/channel-icon/index.vue', () => ({ default: { template: '<span />' } }))
vi.mock('./weixin-qr-login.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@felinic/ui', async (importOriginal) => {
  const { defineComponent, h: rh } = await import('vue')
  const pass = defineComponent({ setup: (_, { slots }) => () => rh('div', [slots.default?.(), slots.trigger?.()]) })
  return {
    ...await importOriginal<object>(),
    ...Object.fromEntries(['ConfirmPopover', 'Dialog', 'DialogBody', 'DialogHeader', 'DialogPanel', 'DialogTitle'].map((k) => [k, pass])),
    Switch: defineComponent({
      props: { modelValue: Boolean, disabled: Boolean },
      emits: ['update:modelValue'],
      setup: (p, { emit }) => () => rh('button', { type: 'button', role: 'switch', 'aria-checked': String(p.modelValue), disabled: p.disabled, onClick: () => emit('update:modelValue', !p.modelValue) }),
    }),
    toast: { success: vi.fn(), error: vi.fn() },
  }
})

const feishuMeta: HandlersChannelMeta = {
  type: 'feishu',
  display_name: 'Feishu',
  config_schema: { version: 2, fields: {
    appId: { type: 'string', required: true, title: 'App ID' },
    appSecret: { type: 'secret', required: true, title: 'App Secret' },
    encryptKey: { type: 'secret', title: 'Encrypt Key' },
    enableStreaming: { type: 'bool', title: 'Stream Replies', example: true },
  } },
}
const makeItem = (credentials?: Record<string, unknown>): BotChannelItem => ({
  meta: feishuMeta, configured: true, config: { id: 'cfg-1', bot_id: 'bot-1', disabled: false, ...(credentials ? { credentials } : {}) },
})

let unmount = () => {}
afterEach(() => {
  unmount()
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

async function renderPanel(initial: BotChannelItem) {
  const itemRef = ref<BotChannelItem>(initial)
  const root = document.body.appendChild(document.createElement('div'))
  const app = createApp({ render: () => h(ChannelSettingsPanel, { botId: 'bot-1', channelItem: itemRef.value }) })
  app.config.globalProperties.$t = (k: string) => k
  app.mount(root)
  unmount = () => app.unmount()
  await nextTick()
  const btn = (text: string) => Array.from(root.querySelectorAll<HTMLButtonElement>('button')).find((b) => b.textContent?.trim() === text)
  return { root, itemRef, btn }
}

describe('ChannelSettingsPanel hydration guard & enableStreaming persistence', () => {
  it('blocks editing and saving while credentials === undefined, then preserves optional fields on save', async () => {
    sdk.putBotsByIdChannelByPlatform.mockResolvedValue({ data: { id: 'cfg-1' } })
    const { root, itemRef, btn } = await renderPanel(makeItem())

    expect(root.textContent).toContain('common.loading')
    expect(root.querySelector('#channel-field-appId')).toBeNull()
    for (const label of ['bots.settings.save', 'bots.channels.actionDisable', 'common.delete']) {
      expect(btn(label)?.disabled).toBe(true)
    }

    itemRef.value = makeItem({ appId: 'cli_real', appSecret: 'sec_real', encryptKey: 'enc_keep', enableStreaming: false })
    await nextTick()

    expect(root.textContent).not.toContain('common.loading')
    expect(root.querySelector<HTMLInputElement>('#channel-field-encryptKey')?.value).toBe('enc_keep')
    expect(root.querySelector('[role="switch"]')?.getAttribute('aria-checked')).toBe('false')
    expect(btn('bots.settings.save')?.disabled).toBe(true)
    expect(btn('bots.channels.actionDisable')?.disabled).toBe(false)

    const appInput = root.querySelector<HTMLInputElement>('#channel-field-appId')!
    appInput.value = 'cli_updated'
    appInput.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    btn('bots.settings.save')!.click()
    await nextTick()

    const callArg = sdk.putBotsByIdChannelByPlatform.mock.calls[0]?.[0] as { path: { id: string; platform: string }; body: ChannelUpsertConfigRequest }
    expect(callArg.path).toEqual({ id: 'bot-1', platform: 'feishu' })
    expect(callArg.body.credentials).toEqual({ appId: 'cli_updated', appSecret: 'sec_real', encryptKey: 'enc_keep', enableStreaming: false })
  })

  it('persists enableStreaming: false when toggled off from legacy config without enableStreaming', async () => {
    sdk.putBotsByIdChannelByPlatform.mockResolvedValue({ data: { id: 'cfg-1' } })
    const { root, btn } = await renderPanel(makeItem({ appId: 'cli_legacy', appSecret: 'sec_legacy' }))

    const toggle = root.querySelector<HTMLButtonElement>('[role="switch"]')!
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    toggle.click()
    await nextTick()
    btn('bots.settings.save')!.click()
    await nextTick()

    const callArg = sdk.putBotsByIdChannelByPlatform.mock.calls[0]?.[0] as { body: ChannelUpsertConfigRequest }
    expect(callArg.body.credentials).toMatchObject({ appId: 'cli_legacy', appSecret: 'sec_legacy', enableStreaming: false })
  })
})
