// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import type { Component, Slots } from 'vue'

const mocks = vi.hoisted(() => ({
  replace: vi.fn(),
  setQueryData: vi.fn(),
  invalidateQueries: vi.fn(),
  getProviders: vi.fn(),
  getProviderTemplates: vi.fn(),
  postProviders: vi.fn(),
  postProvidersFromTemplate: vi.fn(),
  postProvidersByIdImportModels: vi.fn(),
  toastError: vi.fn(),
  calls: [] as string[],
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ replace: mocks.replace }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@pinia/colada', () => ({
  useQueryCache: () => ({
    setQueryData: mocks.setQueryData,
    invalidateQueries: mocks.invalidateQueries,
  }),
}))

vi.mock('@memohai/sdk', () => ({
  getProviders: mocks.getProviders,
  getProviderTemplates: mocks.getProviderTemplates,
  postProviders: mocks.postProviders,
  postProvidersFromTemplate: mocks.postProvidersFromTemplate,
  postProvidersByIdImportModels: mocks.postProvidersByIdImportModels,
}))

vi.mock('@/utils/api-error', () => ({
  resolveApiErrorMessage: (_err: unknown, fallback: string) => fallback,
}))

vi.mock('lucide-vue-next', () => ({ CircleX: () => h('span') }))

vi.mock('@felinic/ui', () => {
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) =>
    h('div', slots.default?.())
  return {
    Card: Passthrough,
    CardContent: Passthrough,
    CardDescription: Passthrough,
    CardFooter: Passthrough,
    CardHeader: Passthrough,
    CardTitle: Passthrough,
    Spinner: () => h('span'),
    Button: (_props: Record<string, unknown>, { slots }: { slots: Slots }) =>
      h('button', slots.default?.()),
    toast: { error: mocks.toastError, success: vi.fn() },
  }
})

function encode(obj: unknown): string {
  return btoa(JSON.stringify(obj)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function linkFor(over: Record<string, unknown>): string {
  return `#payload=${encode({
    v: 1,
    kind: 'memoh-provider-import',
    base_url: 'https://gateway.example.com/v1',
    api_key: 'sk-aaaa-1111',
    client_type: 'openai-completions',
    ...over,
  })}`
}

let ConnectPage: Component

async function mountPage(hash: string) {
  window.history.replaceState(null, '', `/providers/connect${hash}`)
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(ConnectPage)
  app.mount(root)
  await nextTick()
  return { app, root }
}

/** Follow a second import link: same path, new fragment, no reload. */
async function followLink(hash: string) {
  window.history.pushState(null, '', `/providers/connect${hash}`)
  window.dispatchEvent(new Event('hashchange'))
  await nextTick()
}

describe('provider connect page', () => {
  beforeEach(async () => {
    vi.resetModules()
    mocks.calls.length = 0
    mocks.replace.mockReset().mockImplementation(() => void mocks.calls.push('replace'))
    mocks.setQueryData.mockReset().mockImplementation(() => void mocks.calls.push('setQueryData'))
    mocks.invalidateQueries.mockReset()
    mocks.toastError.mockReset()
    mocks.getProviders.mockReset().mockResolvedValue({ data: [{ id: 'created-id', name: 'A' }] })
    mocks.getProviderTemplates.mockReset().mockResolvedValue({ data: [] })
    mocks.postProviders.mockReset().mockResolvedValue({ data: { id: 'created-id' } })
    mocks.postProvidersFromTemplate.mockReset().mockResolvedValue({ data: { id: 'created-id' } })
    mocks.postProvidersByIdImportModels.mockReset().mockResolvedValue({ data: {} })
    ConnectPage = (await import('./connect.vue')).default
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('scrubs the fragment so the key never lingers in history', async () => {
    const { app, root } = await mountPage(linkFor({ name: 'Gateway A' }))
    expect(window.location.hash).toBe('')
    expect(root.textContent).toContain('Gateway A')
    app.unmount()
  })

  it('re-reads the payload when a second link changes only the fragment', async () => {
    const { app, root } = await mountPage(linkFor({ name: 'Gateway A' }))
    expect(root.textContent).toContain('Gateway A')

    await followLink(linkFor({ name: 'Gateway B', api_key: 'sk-bbbb-2222' }))

    // The card must follow the link the user actually clicked, and the second
    // link's key must be scrubbed like the first one's.
    expect(root.textContent).toContain('Gateway B')
    expect(root.textContent).not.toContain('Gateway A')
    expect(root.textContent).toContain('••••2222')
    expect(window.location.hash).toBe('')
    app.unmount()
  })

  it('replaces a stale confirm card with the error of an invalid second link', async () => {
    const { app, root } = await mountPage(linkFor({ name: 'Gateway A' }))
    await followLink('#payload=not-a-valid-payload')

    expect(root.textContent).toContain('providerConnect.invalidTitle')
    expect(root.textContent).not.toContain('Gateway A')
    app.unmount()
  })

  it('stops listening after unmount', async () => {
    const { app } = await mountPage(linkFor({ name: 'Gateway A' }))
    app.unmount()
    // Must not throw on a component that is gone.
    await followLink(linkFor({ name: 'Gateway B' }))
    expect(window.location.hash).toBe(`#payload=${encode({
      v: 1,
      kind: 'memoh-provider-import',
      base_url: 'https://gateway.example.com/v1',
      api_key: 'sk-aaaa-1111',
      client_type: 'openai-completions',
      name: 'Gateway B',
    })}`)
  })

  it('seeds the providers cache before navigating to the new provider', async () => {
    const { app, root } = await mountPage(linkFor({ name: 'Gateway A' }))
    root.querySelectorAll('button')[1]?.click()
    await vi.waitFor(() => expect(mocks.replace).toHaveBeenCalled())

    // Without the seed, the settings page resolves `?provider=created-id`
    // against a persisted list that predates this provider and drops the query.
    expect(mocks.setQueryData).toHaveBeenCalledWith(['providers'], [{ id: 'created-id', name: 'A' }])
    expect(mocks.calls).toEqual(['setQueryData', 'replace'])
    expect(mocks.replace).toHaveBeenCalledWith({
      name: 'providers',
      query: { provider: 'created-id' },
    })
    app.unmount()
  })

  it('still navigates when the seeding request fails', async () => {
    mocks.getProviders.mockRejectedValue(new Error('offline'))
    const { app, root } = await mountPage(linkFor({ name: 'Gateway A' }))
    root.querySelectorAll('button')[1]?.click()
    await vi.waitFor(() => expect(mocks.replace).toHaveBeenCalled())

    expect(mocks.invalidateQueries).toHaveBeenCalledWith({ key: ['providers'] })
    app.unmount()
  })
})
