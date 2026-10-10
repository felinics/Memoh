// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, type App, type Slots } from 'vue'
import WeixinQrLogin from './weixin-qr-login.vue'

const api = vi.hoisted(() => ({ post: vi.fn() }))
vi.mock('@memohai/sdk/client', () => ({ client: { post: api.post } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@felinic/ui', () => ({
  Button: (_props: unknown, { slots, attrs }: { slots: Slots, attrs: Record<string, unknown> }) => h('button', attrs, slots.default?.()),
  Spinner: () => h('span'),
  toast: { success: vi.fn(), error: vi.fn() },
}))

let app: App | null = null
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
})

describe('weixin QR login', () => {
  it('shows local copy, not the thrown text, when starting fails without a code', async () => {
    api.post.mockRejectedValue(new Error('dial tcp 10.0.0.7: connect refused'))
    const root = document.createElement('div')
    document.body.append(root)
    app = createApp(WeixinQrLogin, { botId: 'bot-1' })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    root.querySelector('button')?.click()
    await vi.waitFor(() => expect(root.textContent).toContain('bots.channels.weixinQr.startFailed'))
    expect(root.textContent).not.toContain('10.0.0.7')
    await nextTick()
  })
})
