// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive, type App } from 'vue'
import DependencyConfirmDialog from './dependency-confirm-dialog.vue'

const prepare = vi.hoisted(() => vi.fn())
vi.mock('@/composables/api/useWorkspaceDependencies', () => ({
  prepareDependencyInstallation: prepare,
  prepareDependencyRepair: prepare,
}))
vi.mock('@/composables/useWorkspaceDependencyText', () => ({
  useWorkspaceDependencyText: () => ({ dependencyName: (item: { name: string }) => item.name }),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@felinic/ui', async () => {
  const { Field } = await import('vee-validate')
  const slot = { template: '<div><slot /></div>' }
  return {
    Button: { props: ['loading'], template: '<button><slot /></button>' },
    Dialog: { props: ['open'], template: '<div v-if="open"><slot /></div>' },
    DialogBody: slot, DialogDescription: slot, DialogFooter: slot, DialogHeader: slot, DialogPanel: slot, DialogTitle: slot,
    FormControl: slot, InlineLoadingRow: slot, CalloutBanner: slot,
    FormField: { components: { Field }, props: ['name'], template: '<Field :name="name" v-slot="{ componentField }"><slot :componentField="componentField" /></Field>' },
    FieldStack: slot,
    Input: { props: ['modelValue'], emits: ['update:modelValue'], template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />' },
  }
})

let app: App | undefined
let root: HTMLDivElement
const flush = async () => {
  for (let i = 0; i < 3; i++) {
    await nextTick()
    await vi.runOnlyPendingTimersAsync()
  }
}
function setup() {
  const props = reactive({ open: true, botId: 'bot-1', operation: 'reinstall' as const, mode: 'reinstall' as const, item: { id: 'codex', name: 'Codex', installed_version: '1.2.3', definition_revision: 'recipe-1' } })
  const confirmed = vi.fn()
  root = document.createElement('div')
  document.body.append(root)
  app = createApp({ render: () => h(DependencyConfirmDialog, { ...props, onConfirm: confirmed }) })
  app.mount(root)
  return { props, confirmed }
}
async function submit() {
  root.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
  await flush()
}
beforeEach(() => vi.useFakeTimers())
afterEach(() => { app?.unmount(); root?.remove(); vi.resetAllMocks(); vi.useRealTimers() })

describe('dependency installation confirmation', () => {
  it('reviews an exact older version and requires a second action before authorizing installation', async () => {
    const target = { version: '1.0.0', definition_revision: 'recipe-1', source_url: 'https://example.test/registry' }
    prepare.mockResolvedValue(target)
    const { confirmed } = setup()
    await flush()
    expect(prepare).not.toHaveBeenCalled()
    const input = root.querySelector('input')!
    expect(input.value).toBe('1.2.3')
    input.value = '1.0.0'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await submit()
    expect(prepare).toHaveBeenCalledWith('bot-1', 'codex', 'reinstall', '1.0.0', 'recipe-1')
    expect(confirmed).not.toHaveBeenCalled()
    expect(root.textContent).toContain('1.0.0')
    await submit()
    expect(confirmed).toHaveBeenCalledExactlyOnceWith(target)
    expect(prepare).toHaveBeenCalledTimes(1)
  })

  it('invalidates a reviewed target when the version changes', async () => {
    prepare.mockResolvedValueOnce({ version: '1.2.3', definition_revision: 'recipe-1' })
    prepare.mockResolvedValueOnce({ version: '1.0.0', definition_revision: 'recipe-1' })
    const { confirmed } = setup()
    await flush()
    await submit()
    const input = root.querySelector('input')!
    input.value = '1.0.0'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await submit()
    expect(confirmed).not.toHaveBeenCalled()
    expect(prepare).toHaveBeenCalledTimes(2)
    await submit()
    expect(confirmed).toHaveBeenCalledWith({ version: '1.0.0', definition_revision: 'recipe-1' })
  })

  it('discards a late preparation response after switching bots', async () => {
    let resolve!: (value: unknown) => void
    prepare.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const { props, confirmed } = setup()
    await flush()
    await submit()
    props.botId = 'bot-2'
    await flush()
    resolve({ version: '1.2.3', definition_revision: 'stale-recipe' })
    await flush()
    expect(root.textContent).not.toContain('stale-recipe')
    prepare.mockResolvedValueOnce({ version: '1.2.3', definition_revision: 'recipe-2' })
    await submit()
    expect(confirmed).not.toHaveBeenCalled()
    expect(prepare).toHaveBeenLastCalledWith('bot-2', 'codex', 'reinstall', '1.2.3', 'recipe-1')
  })
})
