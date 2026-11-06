// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

const state = vi.hoisted(() => ({
  start: vi.fn(),
  poll: vi.fn(),
  toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() },
}))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('qrcode', () => ({ default: { toDataURL: vi.fn(async () => 'data:image/png;base64,QR') } }))
vi.mock('@memohai/sdk', () => ({
  postBotsByIdChannelFeishuRegistrationStart: state.start,
  postBotsByIdChannelFeishuRegistrationPoll: state.poll,
}))
vi.mock('@felinic/ui', async () => {
  const { defineComponent, h } = await import('vue')
  const passthrough = defineComponent({
    props: { label: { type: String, default: '' } },
    setup: (props, { slots }) => () => h('div', [props.label, slots.default?.()]),
  })
  const button = defineComponent({
    props: { as: { type: String, default: 'button' }, loading: Boolean },
    emits: ['click'],
    setup: (props, { slots, emit, attrs }) => () => h(props.as, { ...attrs, onClick: () => emit('click') }, slots.default?.()),
  })
  return { SettingsSection: passthrough, SettingsRow: passthrough, Spinner: passthrough, Button: button, toast: state.toast }
})

import FeishuQrRegistration from './feishu-qr-registration.vue'

let unmount = () => {}
let saved = 0

beforeEach(() => {
  vi.useFakeTimers()
  saved = 0
  state.start.mockResolvedValue({
    data: {
      session_id: 'session-1',
      verification_url: 'https://open.feishu.cn/page/launcher?user_code=ABCD',
      expires_in_seconds: 600,
      interval_seconds: 5,
    },
  })
})

afterEach(() => {
  unmount()
  document.body.innerHTML = ''
  vi.useRealTimers()
  vi.clearAllMocks()
})

async function flush() {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

async function render() {
  const target = document.createElement('div')
  document.body.append(target)
  const app = createApp(FeishuQrRegistration, { botId: 'bot-1', onSaved: () => { saved++ } })
  app.config.globalProperties.$t = (key: string) => key
  app.mount(target)
  unmount = () => app.unmount()
  await nextTick()
  return target
}

function buttonWithText(target: HTMLElement, text: string) {
  return Array.from(target.querySelectorAll('button')).find(b => b.textContent?.includes(text))
}

async function startScan(target: HTMLElement) {
  buttonWithText(target, 'bots.channels.feishuQr.start')!.click()
  await flush()
}

async function nextPoll() {
  await vi.advanceTimersByTimeAsync(5000)
  await flush()
}

describe('feishu QR registration', () => {
  it('shows the QR code and polls the session at the server interval', async () => {
    state.poll.mockResolvedValue({ data: { status: 'pending' } })
    const target = await render()
    await startScan(target)

    expect(state.start).toHaveBeenCalledWith(expect.objectContaining({ path: { id: 'bot-1' } }))
    expect(target.querySelector('img')?.getAttribute('src')).toBe('data:image/png;base64,QR')
    expect(target.querySelector('a')?.getAttribute('href')).toBe('https://open.feishu.cn/page/launcher?user_code=ABCD')
    expect(target.textContent).toContain('bots.channels.feishuQr.expiresIn')
    expect(state.poll).not.toHaveBeenCalled()

    await nextPoll()
    expect(state.poll).toHaveBeenCalledTimes(1)
    expect(state.poll).toHaveBeenCalledWith(expect.objectContaining({ body: { session_id: 'session-1' } }))
    await nextPoll()
    expect(state.poll).toHaveBeenCalledTimes(2)
  })

  it.each([
    ['denied', 'bots.channels.feishuQr.denied'],
    ['expired', 'bots.channels.feishuQr.expired'],
    ['failed', 'bots.channels.feishuQr.failed'],
  ])('ends on %s and offers a new QR code', async (status, message) => {
    state.poll.mockResolvedValue({ data: { status } })
    const target = await render()
    await startScan(target)
    await nextPoll()

    expect(target.textContent).toContain(message)
    expect(target.querySelector('img')).toBeNull()
    expect(buttonWithText(target, 'bots.channels.feishuQr.retry')).toBeTruthy()
    await nextPoll()
    expect(state.poll).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['connected', 'success'],
    ['needs_activation', 'warning'],
    ['saved_disabled', 'warning'],
  ] as const)('reports %s and hands the saved channel back to the panel', async (status, toastKind) => {
    state.poll.mockResolvedValue({ data: { status } })
    const target = await render()
    await startScan(target)
    await nextPoll()

    expect(saved).toBe(1)
    expect(state.toast[toastKind]).toHaveBeenCalledTimes(1)
    expect(target.querySelector('img')).toBeNull()
    await nextPoll()
    expect(state.poll).toHaveBeenCalledTimes(1)
  })

  it('retries a transient poll failure but stops on a refused request', async () => {
    state.poll
      .mockRejectedValueOnce({ status: 502 })
      .mockRejectedValueOnce({ status: 409, code: 'channel.already_configured' })
    const target = await render()
    await startScan(target)

    await nextPoll()
    expect(target.querySelector('img')).not.toBeNull()
    await nextPoll()
    expect(state.toast.error).toHaveBeenCalledTimes(1)
    expect(target.textContent).toContain('bots.channels.feishuQr.failed')
    await nextPoll()
    expect(state.poll).toHaveBeenCalledTimes(2)
  })

  it('stops polling when the user cancels', async () => {
    state.poll.mockResolvedValue({ data: { status: 'pending' } })
    const target = await render()
    await startScan(target)
    buttonWithText(target, 'common.cancel')!.click()
    await flush()

    expect(buttonWithText(target, 'bots.channels.feishuQr.start')).toBeTruthy()
    await nextPoll()
    expect(state.poll).not.toHaveBeenCalled()
  })

  it('expires on the local countdown even if the server never answers expired', async () => {
    state.poll.mockResolvedValue({ data: { status: 'pending' } })
    const target = await render()
    await startScan(target)

    await vi.advanceTimersByTimeAsync(600_000)
    await flush()
    expect(target.textContent).toContain('bots.channels.feishuQr.expired')
  })
})
