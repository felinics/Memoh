// @vitest-environment jsdom
import { createApp, h, nextTick, reactive, type App } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'

const bounded = vi.hoisted(() => ({ fetchBoundedBlob: vi.fn(), fetchBoundedText: vi.fn() }))
const toast = vi.hoisted(() => ({ error: vi.fn() }))

vi.mock('@/lib/bounded-fetch', () => bounded)
vi.mock('@/lib/api-client', () => ({
  sdkApiUrl: ({ path }: { path?: { bot_id?: string, content_hash?: string } }) => `/api/bots/${path?.bot_id ?? ''}/media/${path?.content_hash ?? ''}`,
  sdkAuthQuery: () => ({ token: 'tok' }),
}))
vi.mock('@felinic/ui', () => ({
  toast,
  Button: 'button',
  PanePlaceholder: (_p: unknown, { slots }: { slots: { default?: () => unknown, action?: () => unknown } }) =>
    h('div', { 'data-state': 'placeholder' }, [slots.default?.(), slots.action?.()] as never),
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, args?: Record<string, unknown>) => (args?.size ? `${key}:${String(args.size)}` : key) }),
}))
vi.mock('@/components/monaco-editor/index.vue', () => ({
  default: (props: { modelValue?: string }) => h('pre', { 'data-testid': 'monaco' }, props.modelValue),
}))
vi.mock('./panel-frame.vue', () => ({
  default: (_p: unknown, { slots }: { slots: { default?: () => unknown } }) => h('div', { 'data-testid': 'frame' }, slots.default?.() as never),
}))
vi.mock('@/components/file-manager/document-preview.vue', () => ({
  default: (props: { kind?: string, filename?: string }) => h('div', { 'data-testid': 'doc-preview', 'data-kind': props.kind }, props.filename),
}))

import PanelAsset from './panel-asset.vue'

const flush = async () => { await Promise.resolve(); await nextTick(); await Promise.resolve(); await nextTick() }

describe('panel-asset document preview state machine', () => {
  let app: App | null = null
  let host: HTMLDivElement | null = null
  const panelApi = { isVisible: true, onDidVisibilityChange: () => ({ dispose: () => {} }) } as unknown as DockviewPanelApi

  beforeEach(() => { vi.clearAllMocks() })
  afterEach(() => { app?.unmount(); app = null; host?.remove(); host = null })

  it('clears previous documentBlob immediately on oversized switch and retries cleanly after load failure', async () => {
    bounded.fetchBoundedBlob
      .mockResolvedValueOnce({ ok: true, blob: new Blob(['%PDF-1.4']) })
      .mockResolvedValueOnce({ ok: false, reason: 'too-large', maxBytes: 50 * 1024 * 1024 })
      .mockRejectedValueOnce({ code: 'media.asset_not_found', status: 404 })
      .mockResolvedValueOnce({ ok: true, blob: new Blob(['%PDF-1.4-reloaded']) })

    const assetParams = reactive({ name: 'small.pdf', botId: 'bot-1', contentHash: 'hash-small', src: undefined as string | undefined })
    host = document.createElement('div'); document.body.appendChild(host)
    app = createApp({ setup: () => () => h(PanelAsset, { params: { params: assetParams, api: panelApi, containerApi: {} as DockviewApi } }) })
    app.mount(host); await flush()
    expect(host.querySelector('[data-testid="doc-preview"]')?.textContent).toBe('small.pdf')

    assetParams.name = 'large.pdf'; assetParams.contentHash = 'hash-large'; await flush()
    expect(host.querySelector('[data-testid="doc-preview"]')).toBeNull()
    expect(host.textContent).toContain('bots.files.previewTooLarge:50.0 MB')

    assetParams.name = 'broken.pdf'; assetParams.contentHash = 'hash-broken'; await flush()
    expect(host.textContent).toContain('bots.files.previewFailed')
    expect(toast.error).toHaveBeenCalledTimes(1)

    assetParams.name = 'small.pdf'; assetParams.contentHash = 'hash-small'; await flush()
    expect(host.querySelector('[data-testid="doc-preview"]')?.textContent).toBe('small.pdf')
    expect(bounded.fetchBoundedBlob).toHaveBeenCalledTimes(4)
  })
})
