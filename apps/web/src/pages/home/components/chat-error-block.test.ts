// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import ja from '@/i18n/locales/ja.json'
import zh from '@/i18n/locales/zh.json'
import ChatErrorBlock from './chat-error-block.vue'

const push = vi.hoisted(() => vi.fn())
vi.mock('vue-router', () => ({ useRouter: () => ({ push }) }))
vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })

let app: App | undefined
let host: HTMLElement

function mountBlock(opts: {
  locale?: 'en' | 'zh' | 'ja'
  code?: string
  message?: string
  requestId?: string
  traceId?: string
  slot?: () => unknown
} = {}) {
  const i18n = createI18n({
    legacy: false,
    locale: opts.locale ?? 'en',
    messages: { en, ja, zh },
  })
  const blockProps = {
    message: opts.message ?? 'copy of the error',
    error: {
      type: 'error',
      code: opts.code,
      request_id: opts.requestId,
      trace_id: opts.traceId,
      content: '',
    },
  }
  app = createApp(
    opts.slot
      ? { render: () => h(ChatErrorBlock, blockProps, { default: opts.slot }) }
      : ChatErrorBlock,
    opts.slot ? undefined : blockProps,
  )
  app.use(i18n)
  host = document.createElement('div')
  document.body.appendChild(host)
  app.mount(host)
  return host
}

beforeEach(() => {
  push.mockReset()
})

afterEach(() => {
  app?.unmount()
  host?.remove()
})

const buttons = () => [...host.querySelectorAll('button, a')].map(el => el.textContent?.trim())
const copyButton = () => host.querySelector('button:has([data-slot="label-swap"])') as HTMLButtonElement | null

describe('ChatErrorBlock', () => {
  it.each([
    ['agent.provider_auth_failed', 'Provider credentials were rejected'],
    ['agent.provider_permission_denied', 'Provider credentials were rejected'],
    ['agent.provider_quota_exhausted', 'Provider balance is exhausted'],
    ['agent.provider_request_rejected', 'Model provider rejected the request'],
  ])('renders Provider settings and ghost Copy for %s', async (code, expectedTitle) => {
    mountBlock({ code, requestId: 'req-provider' })
    await nextTick()

    expect(host.textContent).toContain(expectedTitle)
    expect(buttons()).toContain('Provider settings')
    expect(buttons()).toContain('Details')
    expect(copyButton()?.dataset.variant).toBe('ghost')

    const settingsButton = [...host.querySelectorAll('button')].find(el => el.textContent?.trim() === 'Provider settings')!
    settingsButton.click()
    expect(push).toHaveBeenCalledWith('/settings/providers')
  })

  it('renders secondary Copy, hides Details without IDs, and expands Request ID when present', async () => {
    mountBlock({ code: 'agent.response_interrupted' })
    await nextTick()
    expect(host.textContent).toContain('Response was interrupted')
    expect(copyButton()?.dataset.variant).toBe('secondary')
    expect(buttons()).not.toContain('Provider settings')
    expect(buttons()).not.toContain('Details')

    app?.unmount()
    host?.remove()

    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      configurable: true,
    })

    mountBlock({
      code: 'agent.response_timeout',
      message: 'The model did not respond in time.',
      requestId: 'req-timeout-1',
    })
    await nextTick()

    expect(host.textContent).toContain('Couldn\'t get a response in time')
    expect(copyButton()?.dataset.variant).toBe('secondary')
    const detailsButton = [...host.querySelectorAll('button')].find(el => el.textContent?.trim() === 'Details')!
    expect(detailsButton.getAttribute('aria-expanded')).toBe('false')

    detailsButton.click()
    await nextTick()
    expect(detailsButton.getAttribute('aria-expanded')).toBe('true')
    expect(host.textContent).toContain('Request ID: req-timeout-1')

    copyButton()!.click()
    await Promise.resolve()
    await nextTick()
    expect(writeText).toHaveBeenCalledWith('The model did not respond in time.\nRequest ID: req-timeout-1')
  })

  it('replaces Provider settings when a custom recovery slot action is provided', async () => {
    mountBlock({
      code: 'external_runtime.auth_required',
      slot: () => h('button', 'Open Agent settings'),
    })
    await nextTick()

    expect(host.textContent).toContain('Agent account needs sign-in')
    expect(buttons()).toContain('Open Agent settings')
    expect(buttons()).not.toContain('Provider settings')
    expect(copyButton()?.dataset.variant).toBe('ghost')
  })

  it.each([
    ['zh', '模型服务响应超时', '复制', '详情'],
    ['ja', 'モデルの応答がタイムアウトしました', 'コピー', '詳細'],
  ] as const)('localizes headings and actions for %s', async (locale, expectedTitle, expectedCopy, expectedDetails) => {
    mountBlock({
      locale,
      code: 'agent.response_timeout',
      traceId: 'trace-99',
    })
    await nextTick()

    expect(host.textContent).toContain(expectedTitle)
    expect(host.textContent).toContain(expectedCopy)
    expect(buttons()).toContain(expectedDetails)
  })
})
