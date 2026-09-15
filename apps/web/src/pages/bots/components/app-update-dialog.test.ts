// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive, ref, type App } from 'vue'
import type { AppItem } from '@/composables/api/useApps'
import AppUpdateDialog from './app-update-dialog.vue'

const mocks = vi.hoisted(() => ({ prepare: vi.fn(), confirm: vi.fn() }))
vi.mock('@memohai/sdk', () => ({ postBotsByBotIdAppsPrepare: mocks.prepare }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref('en') }) }))
vi.mock('@pinia/colada', () => ({ useQuery: vi.fn(), useQueryCache: vi.fn() }))
vi.mock('@/utils/api-error', () => ({ resolveApiErrorMessage: (_: unknown, fallback: string) => fallback }))
vi.mock('@felinic/ui', () => {
  const slot = { template: '<div><slot /></div>' }
  return {
    Dialog: { props: ['open'], template: '<div v-if="open"><slot /></div>' },
    Button: { props: ['disabled', 'loading'], template: '<button :disabled="disabled || loading"><slot /></button>' },
    Checkbox: { props: ['modelValue'], emits: ['update:modelValue'], template: '<input type="checkbox" :checked="modelValue" @change="$emit(\'update:modelValue\', $event.target.checked)" />' },
    DialogBody: slot, DialogDescription: slot, DialogFooter: slot, DialogHeader: slot, DialogPanel: slot, DialogTitle: slot,
  }
})

let app: App
let root: HTMLDivElement
let props: { open: boolean; botId: string; item: AppItem; action: 'update' | 'resume' }
const confirmation = { dependency_id: 'codex', action: 'update', version: '0.154.0', definition_revision: 'recipe-1' }

beforeEach(() => {
  vi.clearAllMocks()
  mocks.prepare.mockResolvedValue({ data: { registry_id: 'memoh', app_id: 'codex', revision: 'app-2', dependencies: [confirmation] } })
  root = document.createElement('div')
  document.body.append(root)
  props = reactive({
    open: true, botId: 'bot-a', action: 'update',
    item: { registry_id: 'memoh', app_id: 'codex', installation_id: 'installation', revision: 'app-1', available_revision: 'app-2', dependencies: [] },
  })
})
afterEach(() => { app.unmount(); root.remove() })
function mount() {
  app = createApp({ render: () => h(AppUpdateDialog, { ...props, onConfirm: mocks.confirm }) })
  app.mount(root)
}
function click(label: string) {
  const button = [...root.querySelectorAll('button')].find(button => button.textContent?.trim() === label)
  expect(button).toBeDefined()
  button!.click()
}

it('prepares selected updates and emits only the frozen release and dependency confirmation', async () => {
  mount()
  expect(mocks.prepare).not.toHaveBeenCalled()
  click('apps.prepare.review')
  await vi.waitFor(() => expect(root.textContent).toContain('recipe-1'))
  expect(root.textContent).toContain('app-2')
  expect(mocks.confirm).not.toHaveBeenCalled()
  expect(mocks.prepare).toHaveBeenCalledWith(expect.objectContaining({ body: {
    action: 'update', registry_id: 'memoh', app_id: 'codex', release: true, dependencies: [],
  } }))
  click('apps.update.confirm')
  expect(mocks.confirm).toHaveBeenCalledWith({
    action: 'update', release: true, dependencies: [], releaseRevision: 'app-2', dependencyConfirmations: [confirmation],
  })
})

it('invalidates preparation when the selected release changes', async () => {
  mount()
  click('apps.prepare.review')
  await vi.waitFor(() => expect(root.textContent).toContain('recipe-1'))
  const checkbox = root.querySelector('input')!
  checkbox.click()
  await nextTick()
  expect(root.textContent).not.toContain('recipe-1')
  expect(mocks.confirm).not.toHaveBeenCalled()
  const review = [...root.querySelectorAll('button')].find(button => button.textContent?.trim() === 'apps.prepare.review')!
  expect(review.disabled).toBe(true)
})

it('requires preparation before resuming and returns the exact installed App revision', async () => {
  props.action = 'resume'
  mount()
  click('apps.prepare.review')
  await vi.waitFor(() => expect(root.textContent).toContain('recipe-1'))
  expect(mocks.prepare).toHaveBeenCalledWith(expect.objectContaining({ body: { action: 'resume', installation_id: 'installation' } }))
  expect(mocks.confirm).not.toHaveBeenCalled()
  click('apps.action.resume')
  expect(mocks.confirm).toHaveBeenCalledWith({
    action: 'resume', release: false, dependencies: [], releaseRevision: 'app-2', dependencyConfirmations: [confirmation],
  })
})
