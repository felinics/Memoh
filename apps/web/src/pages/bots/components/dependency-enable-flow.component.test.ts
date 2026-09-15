// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive, ref, type App } from 'vue'
import type { BotagentsBotAgent } from '@memohai/sdk'
import DependencyEnableFlow from './dependency-enable-flow.vue'

const mocks = vi.hoisted(() => ({ preflight: vi.fn(), app: vi.fn(), prepare: vi.fn(), start: vi.fn(), view: vi.fn(), unview: vi.fn(), error: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  getSupermarketRegistriesByRegistryIdAppsByAppId: mocks.app,
  postBotsByBotIdAppsPrepare: mocks.prepare,
  postBotsByBotIdContainerStart: vi.fn(),
}))
vi.mock('@/composables/api/useWorkspaceDependencies', () => ({ preflightDependencies: mocks.preflight }))
vi.mock('@/store/app-operations', () => ({ useAppOperationsStore: () => ({ start: mocks.start, get: vi.fn(), view: mocks.view, unview: mocks.unview }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref('en') }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ replace: vi.fn() }), useRoute: () => ({ query: {} }) }))
vi.mock('@pinia/colada', () => ({ useQuery: vi.fn(), useQueryCache: vi.fn() }))
vi.mock('@/utils/api-error', () => ({ resolveApiErrorMessage: (_: unknown, fallback: string) => fallback }))
vi.mock('./app-progress-dialog.vue', () => ({ default: { render: () => null } }))
vi.mock('@felinic/ui', () => {
  const slot = { template: '<div><slot /></div>' }
  return {
    Dialog: { props: ['open'], template: '<div v-if="open"><slot /></div>' },
    Button: { props: ['disabled', 'loading'], template: '<button :disabled="disabled || loading"><slot /></button>' },
    DialogBody: slot, DialogDescription: slot, DialogFooter: slot, DialogHeader: slot, DialogPanel: slot, DialogTitle: slot,
    toast: { error: mocks.error, success: vi.fn() },
  }
})

let app: App
let root: HTMLDivElement
let props: { botId: string }
let flow: { run: (agent: BotagentsBotAgent) => Promise<boolean> }
const agent = { dependency: { dependency_id: 'codex' } } as BotagentsBotAgent
const confirmation = { dependency_id: 'codex', action: 'install', version: '0.154.0', definition_revision: 'recipe-1' }

beforeEach(() => {
  vi.clearAllMocks()
  mocks.preflight.mockResolvedValue({ workspace_state: 'running', items: [{ dependency_id: 'codex', state: 'missing' }] })
  mocks.app.mockResolvedValue({ data: { registry_id: 'memoh', app_id: 'codex', name: 'Codex', revision: 'app-1' } })
  mocks.prepare.mockResolvedValue({ data: { registry_id: 'memoh', app_id: 'codex', revision: 'app-1', dependencies: [confirmation] } })
  mocks.start.mockReturnValue({ kind: 'started', operation: { key: 'bot-a/memoh/codex', status: 'running', name: 'Codex', steps: [], lines: [] } })
  root = document.createElement('div')
  document.body.append(root)
  props = reactive({ botId: 'bot-a' })
  app = createApp({ render: () => h(DependencyEnableFlow, { ...props, ref: (instance: unknown) => { if (instance) flow = instance as typeof flow } }) })
  app.mount(root)
})
afterEach(() => { app.unmount(); root.remove() })
function click(label: string) {
  const button = [...root.querySelectorAll('button')].find(button => button.textContent?.trim() === label)
  expect(button).toBeDefined()
  button!.click()
}

it('does not install on preflight or prepare, and confirms the displayed canonical App dependencies', async () => {
  void flow.run(agent)
  await vi.waitFor(() => expect(root.textContent).toContain('apps.prepare.review'))
  expect(mocks.prepare).not.toHaveBeenCalled()
  expect(mocks.start).not.toHaveBeenCalled()
  click('apps.prepare.review')
  await vi.waitFor(() => expect(root.textContent).toContain('recipe-1'))
  expect(root.textContent).toContain('0.154.0')
  expect(mocks.start).not.toHaveBeenCalled()
  click('bots.dependencies.confirm.installAndEnable')
  expect(mocks.start).toHaveBeenCalledWith(expect.objectContaining({
    botId: 'bot-a', action: 'install', install: { registryId: 'memoh', appId: 'codex', revision: 'app-1' }, dependencyConfirmations: [confirmation],
  }))
})

it('cancels the enable attempt when its bot changes during App lookup', async () => {
  let resolveApp!: (value: unknown) => void
  mocks.app.mockReturnValueOnce(new Promise(done => { resolveApp = done }))
  const completed = flow.run(agent)
  await vi.waitFor(() => expect(root.textContent).toContain('apps.prepare.review'))
  click('apps.prepare.review')
  props.botId = 'bot-b'
  await nextTick()
  expect(await completed).toBe(false)
  resolveApp({ data: { revision: 'stale-app' } })
  await nextTick()
  await nextTick()
  expect(mocks.prepare).not.toHaveBeenCalled()
  expect(mocks.start).not.toHaveBeenCalled()
})

it('allows retry after preparation fails without enabling or starting installation', async () => {
  mocks.prepare.mockRejectedValueOnce(new Error('offline'))
  void flow.run(agent)
  await vi.waitFor(() => expect(root.textContent).toContain('apps.prepare.review'))
  click('apps.prepare.review')
  await vi.waitFor(() => expect(root.textContent).toContain('apps.prepare.failed'))
  expect(mocks.start).not.toHaveBeenCalled()
  click('apps.prepare.review')
  await vi.waitFor(() => expect(root.textContent).toContain('recipe-1'))
  expect(mocks.start).not.toHaveBeenCalled()
})
