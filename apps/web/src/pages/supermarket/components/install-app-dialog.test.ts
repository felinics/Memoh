// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive, ref, type App } from 'vue'
import type { HandlersSupermarketAppDescriptor } from '@memohai/sdk'
import InstallAppDialog from './install-app-dialog.vue'

const mocks = vi.hoisted(() => ({ prepare: vi.fn(), start: vi.fn(), push: vi.fn() }))
vi.mock('@memohai/sdk', () => ({ postBotsByBotIdAppsPrepare: mocks.prepare, getConnectorsCatalog: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref('en') }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: mocks.push }) }))
vi.mock('@pinia/colada', () => ({ useQuery: () => ({ data: ref([]) }), useQueryCache: vi.fn() }))
vi.mock('@/utils/api-error', () => ({ resolveApiErrorMessage: (_: unknown, fallback: string) => fallback }))
vi.mock('@/composables/useConnectorOAuth', () => ({ prepareConnectorOAuthPopup: vi.fn() }))
vi.mock('@/components/bot-select/index.vue', () => ({ default: { render: () => null } }))
vi.mock('@/pages/bots/components/app-progress-dialog.vue', () => ({ default: { render: () => null } }))
vi.mock('@/pages/bots/composables/useAppOperation', () => ({ useAppOperation: () => ({
  active: ref(null), progressOpen: ref(false), start: mocks.start, retry: vi.fn(), setProgressOpen: vi.fn(),
}) }))
vi.mock('@felinic/ui', () => {
  const slot = { template: '<div><slot /></div>' }
  return {
    Dialog: { props: ['open'], template: '<div v-if="open"><slot /></div>' },
    Button: { props: ['disabled', 'loading'], template: '<button :disabled="disabled || loading"><slot /></button>' },
    DialogBody: slot, DialogClose: slot, DialogDescription: slot, DialogFooter: slot, DialogHeader: slot,
    DialogPanel: slot, DialogTitle: slot, FieldStack: slot,
  }
})

let app: App
let root: HTMLDivElement
let props: { open: boolean; pkg: HandlersSupermarketAppDescriptor; defaultBotId: string; lockBot: boolean }
const confirmation = { dependency_id: 'codex', action: 'install', version: '0.154.0', definition_revision: 'recipe-1', registry_id: 'memoh', source_url: 'https://example.org/registry', manifest_digest: 'digest-1' }

beforeEach(() => {
  vi.clearAllMocks()
  mocks.prepare.mockResolvedValue({ data: { registry_id: 'memoh', app_id: 'codex', revision: 'app-1', dependencies: [confirmation] } })
  mocks.start.mockReturnValue(true)
  root = document.createElement('div')
  document.body.append(root)
  props = reactive({
    open: true, defaultBotId: 'bot-a', lockBot: true,
    pkg: { registry_id: 'memoh', app_id: 'codex', name: 'Codex', revision: 'app-1', dependencies: ['codex'], skills: [], connectors: [] } as unknown as HandlersSupermarketAppDescriptor,
  })
  app = createApp({ render: () => h(InstallAppDialog, { ...props, 'onUpdate:open': (open: boolean) => { props.open = open } }) })
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
})
afterEach(() => { app.unmount(); root.remove() })

function click(label: string) {
  const button = [...root.querySelectorAll('button')].find(button => button.textContent?.trim() === label)
  expect(button).toBeDefined()
  button!.click()
}

it('shows exact dependency versions and recipes before allowing installation from the sidebar or market', async () => {
  expect(mocks.prepare).not.toHaveBeenCalled()
  expect(mocks.start).not.toHaveBeenCalled()
  click('apps.prepare.review')
  await vi.waitFor(() => expect(root.textContent).toContain('recipe-1'))
  expect(root.textContent).toContain('0.154.0')
  expect(mocks.start).not.toHaveBeenCalled()
  click('supermarket.install')
  expect(mocks.start).toHaveBeenCalledOnce()
  expect(mocks.start).toHaveBeenCalledWith(expect.objectContaining({
    install: { registryId: 'memoh', appId: 'codex', revision: 'app-1' }, dependencyConfirmations: [confirmation],
  }))
})

it('invalidates an accepted preparation when the originating bot changes', async () => {
  click('apps.prepare.review')
  await vi.waitFor(() => expect(root.textContent).toContain('recipe-1'))
  props.defaultBotId = 'bot-b'
  await nextTick()
  expect(root.textContent).not.toContain('recipe-1')
  expect(mocks.start).not.toHaveBeenCalled()
  click('apps.prepare.review')
  await vi.waitFor(() => expect(mocks.prepare).toHaveBeenCalledTimes(2))
  expect(mocks.prepare).toHaveBeenLastCalledWith(expect.objectContaining({ path: { bot_id: 'bot-b' } }))
})

it('does not restore a pending preparation after closing and reopening the dialog', async () => {
  let resolve!: (value: unknown) => void
  mocks.prepare.mockReturnValueOnce(new Promise(done => { resolve = done }))
  click('apps.prepare.review')
  props.open = false
  await nextTick()
  props.open = true
  await nextTick()
  resolve({ data: { revision: 'app-1', dependencies: [confirmation] } })
  await nextTick()
  await nextTick()
  expect(root.textContent).not.toContain('recipe-1')
  expect(mocks.start).not.toHaveBeenCalled()
})
