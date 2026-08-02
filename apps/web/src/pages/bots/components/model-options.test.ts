// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */

import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

const SlotComponent = (name: string) => defineComponent({
  name,
  setup(_, { slots }) {
    return () => h('div', slots.default?.())
  },
})

const EmptyComponent = (name: string) => defineComponent({
  name,
  setup() {
    return () => h('span')
  },
})

vi.mock('@felinic/ui', () => ({
  Popover: SlotComponent('Popover'),
  PopoverAnchor: SlotComponent('PopoverAnchor'),
  PopoverContent: SlotComponent('PopoverContent'),
  menuChromeClass: 'menu-chrome',
  menuItemClass: 'menu-item',
  menuLabelClass: 'menu-label',
  menuSearchHeaderClass: 'menu-search-header',
  menuSearchInputClass: 'menu-search-input',
  menuSeparatorClass: 'menu-separator',
  virtualListboxClass: 'virtual-listbox',
}))

vi.mock('lucide-vue-next', () => ({
  Check: EmptyComponent('Check'),
  ChevronRight: EmptyComponent('ChevronRight'),
  Lightbulb: EmptyComponent('Lightbulb'),
}))

vi.mock('@tanstack/vue-virtual', () => ({
  useVirtualizer: () => ref({
    getTotalSize: () => 38,
    getVirtualItems: () => [{ index: 0, key: 'model-1', start: 0 }],
    measureElement: vi.fn(),
    scrollToOffset: vi.fn(),
    scrollToIndex: vi.fn(),
  }),
}))

vi.mock('@/components/model-description-tooltip/index.vue', () => ({
  default: defineComponent({
    name: 'ModelDescriptionTooltip',
    props: {
      description: {
        type: String,
        default: undefined,
      },
      open: Boolean,
    },
    emits: ['update:open'],
    setup(props, { emit, slots }) {
      return () => h('div', {
        'data-model-tooltip': '',
        'data-open': String(props.open),
        onPointerenter: () => emit('update:open', true),
      }, slots.default?.())
    },
  }),
}))

describe('ModelOptions', () => {
  let app: ReturnType<typeof createApp> | undefined
  let root: HTMLDivElement | undefined

  afterEach(() => {
    app?.unmount()
    root?.remove()
    app = undefined
    root = undefined
  })

  async function mountPicker(overrides: Record<string, unknown> = {}) {
    const ModelOptions = (await import('./model-options.vue')).default
    root = document.createElement('div')
    document.body.append(root)
    app = createApp(ModelOptions, {
      models: [{
        id: 'model-1',
        model_id: 'model-1',
        name: 'Model 1',
        provider_id: '',
        type: 'chat',
        config: { description: 'Model description' },
      }],
      providers: [],
      modelType: 'chat',
      open: true,
      modelValue: 'model-1',
      showReasoning: true,
      reasoningEffort: 'disable',
      ...overrides,
    })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await nextTick()
    await nextTick()
    return root
  }

  it('dismisses an open model description when the list scrolls', async () => {
    const el = await mountPicker()
    const tooltip = el.querySelector<HTMLElement>('[data-model-tooltip]')
    const listbox = el.querySelector<HTMLElement>('[role="listbox"]')

    expect(tooltip).not.toBeNull()
    expect(listbox).not.toBeNull()

    tooltip!.dispatchEvent(new Event('pointerenter'))
    await nextTick()
    expect(tooltip!.dataset.open).toBe('true')

    listbox!.dispatchEvent(new Event('scroll'))
    await nextTick()
    expect(tooltip!.dataset.open).toBe('false')
  })

  it('renders and emits agent-provided reasoning options without guessing fixed levels', async () => {
    const updateReasoning = vi.fn()
    const el = await mountPicker({
      reasoningEffort: 'ultra',
      reasoningOptions: [
        { value: 'balanced', label: 'Balanced by agent' },
        { value: 'ultra', label: 'Maximal deliberation', description: 'Agent-defined option' },
      ],
      'onUpdate:reasoningEffort': updateReasoning,
    })

    expect(el.textContent).toContain('Maximal deliberation')
    const option = Array.from(el.querySelectorAll('button')).find(button => button.textContent?.includes('Balanced by agent'))
    expect(option).toBeDefined()
    option!.click()
    await nextTick()
    expect(updateReasoning).toHaveBeenCalledWith('balanced')
  })

  it('omits the reasoning footer unless the consumer opts in', async () => {
    // The embedding/tts pickers share this component and must stay a plain model
    // list — no effort row, no flyout.
    const el = await mountPicker({ showReasoning: false })
    expect(el.querySelector('[aria-label="chat.reasoningEffort"]')).toBeNull()
  })

  it('keeps the model list keyboard-navigable', async () => {
    // The merged picker replaced a Chat-only implementation that had no keyboard
    // support; losing the listbox roving focus here would be a silent regression.
    const el = await mountPicker()
    const input = el.querySelector<HTMLInputElement>('input[role="combobox"]')
    expect(input).not.toBeNull()

    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    await nextTick()
    expect(el.querySelector('[data-highlighted]')).not.toBeNull()
  })

  it('commits a model selection through the v-model contract', async () => {
    const updateModel = vi.fn()
    const el = await mountPicker({ modelValue: '', 'onUpdate:modelValue': updateModel })
    const option = Array.from(el.querySelectorAll('button')).find(button => button.textContent?.includes('Model 1'))
    expect(option).toBeDefined()
    option!.click()
    await nextTick()
    expect(updateModel).toHaveBeenCalledWith('model-1')
  })
})
