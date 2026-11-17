// @vitest-environment jsdom
import { KeepAlive, createApp, h, nextTick, reactive, ref, type App } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const docxSandbox = vi.hoisted(() => ({
  renderDocxBlobToSrcdoc: vi.fn(),
  normalizeSafeExternalHref: (raw: unknown) => {
    const v = typeof raw === 'string' ? raw.trim() : ''
    return v.startsWith('https://') || v.startsWith('http://') || v.startsWith('mailto:') ? v : null
  },
  DOCX_EXTERNAL_LINK_MESSAGE_TYPE: 'memoh:docx-open-external',
}))

vi.mock('./docx-sandbox', () => docxSandbox)
vi.mock('@felinic/ui', () => ({
  PanePlaceholder: (props: { loading?: boolean }, { slots }: { slots: { default?: () => unknown } }) =>
    h('div', { 'data-testid': props.loading ? 'loading' : 'placeholder' }, slots.default?.() as never),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

import DocumentPreview from './document-preview.vue'
import type { DocumentPreviewKind } from './utils'

const flush = async () => { await Promise.resolve(); await nextTick(); await Promise.resolve(); await nextTick() }

describe('DocumentPreview component', () => {
  let app: App | null = null
  let host: HTMLDivElement | null = null
  let revokedUrls: string[] = []

  const mountRoot = async (renderFn: () => ReturnType<typeof h>) => {
    host = document.createElement('div')
    document.body.appendChild(host)
    app = createApp({ setup: () => renderFn })
    app.mount(host)
    await flush()
  }

  beforeEach(() => {
    vi.clearAllMocks(); revokedUrls = []
    let n = 0
    vi.spyOn(URL, 'createObjectURL').mockImplementation(() => `blob:http://localhost/mock-pdf-${++n}`)
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation((u: string) => { revokedUrls.push(u) })
    docxSandbox.renderDocxBlobToSrcdoc.mockImplementation(async (_b: Blob, opts: { channelToken: string }) => ({
      ok: true, srcdoc: `<!doctype html><html><body data-token="${opts.channelToken}">DOCX READY</body></html>`,
    }))
  })

  afterEach(() => {
    app?.unmount(); app = null; host?.remove(); host = null
    delete (window as { api?: unknown }).api
    vi.restoreAllMocks()
  })

  it('switches cleanly across docx -> pdf -> docx without allow-same-origin and revokes old blob URLs', async () => {
    const state = reactive<{ blob: Blob, kind: DocumentPreviewKind, filename: string }>({ blob: new Blob(['d1']), kind: 'docx', filename: '1.docx' })
    await mountRoot(() => h(DocumentPreview, state))
    expect(host!.querySelector('iframe')?.getAttribute('sandbox')).toBe('allow-scripts')
    expect(host!.querySelector('iframe')?.getAttribute('srcdoc')).toContain('DOCX READY')

    state.kind = 'pdf'; state.blob = new Blob(['%PDF-1.4']); await flush()
    expect(host!.querySelector('iframe')?.getAttribute('sandbox')).toBeNull()
    expect(host!.querySelector('iframe')?.getAttribute('src')).toBe('blob:http://localhost/mock-pdf-1')

    docxSandbox.renderDocxBlobToSrcdoc.mockResolvedValueOnce({ ok: true, srcdoc: '<html><body>SECOND DOCX</body></html>' })
    state.kind = 'docx'; state.blob = new Blob(['d2']); await flush()
    expect(revokedUrls).toContain('blob:http://localhost/mock-pdf-1')
    expect(host!.querySelector('iframe')?.getAttribute('srcdoc')).toContain('SECOND DOCX')
  })

  it('validates postMessage source, token, and URL protocol before opening external links', async () => {
    const openExternalUrl = vi.fn().mockResolvedValue(undefined)
    ;(window as unknown as { api: unknown }).api = { desktop: { openExternalUrl } }
    let token = ''
    docxSandbox.renderDocxBlobToSrcdoc.mockImplementationOnce(async (_b: Blob, opts: { channelToken: string }) => {
      token = opts.channelToken; return { ok: true, srcdoc: '<html><body>docx</body></html>' }
    })
    await mountRoot(() => h(DocumentPreview, { blob: new Blob(['x']), kind: 'docx', filename: 'a.docx' }))

    const fakeWin = {} as Window
    Object.defineProperty(host!.querySelector('iframe')!, 'contentWindow', { value: fakeWin, configurable: true })
    window.dispatchEvent(new MessageEvent('message', { source: window, data: { type: 'memoh:docx-open-external', token, href: 'https://memoh.ai' } }))
    window.dispatchEvent(new MessageEvent('message', { source: fakeWin, data: { type: 'memoh:docx-open-external', token: 'wrong', href: 'https://memoh.ai' } }))
    window.dispatchEvent(new MessageEvent('message', { source: fakeWin, data: { type: 'memoh:docx-open-external', token, href: 'javascript:alert(1)' } }))
    expect(openExternalUrl).not.toHaveBeenCalled()

    window.dispatchEvent(new MessageEvent('message', { source: fakeWin, data: { type: 'memoh:docx-open-external', token, href: 'https://memoh.ai/docs' } }))
    expect(openExternalUrl).toHaveBeenCalledWith('https://memoh.ai/docs')
  })

  it('defers KeepAlive deactivated renders and displays localized fallbacks on too-large or invalid', async () => {
    const active = ref(true); const blob = ref(new Blob(['v1']))
    await mountRoot(() => h(KeepAlive, null, [active.value ? h(DocumentPreview, { key: 'doc', blob: blob.value, kind: 'docx', filename: 'r.docx' }) : null]))
    expect(docxSandbox.renderDocxBlobToSrcdoc).toHaveBeenCalledTimes(1)

    active.value = false; await flush(); blob.value = new Blob(['v2']); await flush()
    expect(docxSandbox.renderDocxBlobToSrcdoc).toHaveBeenCalledTimes(1)

    docxSandbox.renderDocxBlobToSrcdoc.mockResolvedValueOnce({ ok: false, reason: 'too-large' })
    active.value = true; await flush()
    expect(host!.textContent).toContain('bots.files.previewTooLarge')

    docxSandbox.renderDocxBlobToSrcdoc.mockResolvedValueOnce({ ok: false, reason: 'invalid' })
    blob.value = new Blob(['v3']); await flush()
    expect(host!.textContent).toContain('bots.files.previewFailed')
  })
})
