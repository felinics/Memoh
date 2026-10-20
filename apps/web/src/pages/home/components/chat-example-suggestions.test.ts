// @vitest-environment jsdom

import { createApp, h, ref, type VNode } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { StarterPromptSet } from '@/composables/api/useStarterPrompts'

const state = vi.hoisted(() => ({
  set: undefined as unknown as { value: StarterPromptSet | undefined },
  dismissed: undefined as unknown as { value: boolean },
}))

vi.mock('@felinic/ui', () => {
  const element = (tag: string) => ({
    inheritAttrs: false,
    setup(_: unknown, context: { attrs: Record<string, unknown>, slots: { default?: () => VNode[] } }) {
      return () => h(tag, context.attrs, context.slots.default?.())
    },
  })
  return { Button: element('button'), TextButton: element('button'), toast: { error: vi.fn() } }
})
vi.mock('lucide-vue-next', () => ({ X: { render: () => h('svg') } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/chat-examples/use-chat-examples-ui', () => ({
  useChatExamplesUi: () => ({ welcomeDismissed: state.dismissed }),
}))
vi.mock('@/composables/useComposerPrefill', () => ({ useComposerPrefill: () => ({ prefillComposer: vi.fn() }) }))
vi.mock('@/composables/api/useStarterPrompts', () => ({
  useStarterPromptsQuery: () => ({
    data: state.set,
    status: ref('success'),
    refresh: vi.fn(),
    refreshing: ref(false),
  }),
}))
// The list is covered by its own test; here it only reports what it was given.
vi.mock('./starter-prompt-list.vue', () => ({
  default: {
    props: ['rows', 'enterHint', 'notice'],
    setup(props: { rows: unknown[] | null, notice?: { title: string, description: string, to?: unknown } | null }) {
      return () => h('div', {
        'data-list': '',
        'data-rows': props.rows?.length ?? 'loading',
        'data-notice': props.notice ? JSON.stringify(props.notice) : undefined,
      })
    },
  },
}))

import ChatExampleSuggestions from './chat-example-suggestions.vue'

describe('ChatExampleSuggestions', () => {
  let app: ReturnType<typeof createApp> | undefined
  let root: HTMLDivElement | undefined

  beforeEach(() => {
    state.set = ref<StarterPromptSet | undefined>()
    state.dismissed = ref(false)
  })

  afterEach(() => {
    app?.unmount()
    root?.remove()
    app = undefined
    root = undefined
  })

  function mount(set: StarterPromptSet) {
    state.set.value = set
    root = document.createElement('div')
    document.body.append(root)
    app = createApp({ render: () => h(ChatExampleSuggestions, { botId: 'bot-1', composerReplaceable: true }) })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    const section = () => root!.querySelector('[data-welcome-suggestions]')
    const notice = () => {
      const raw = root!.querySelector<HTMLElement>('[data-list]')?.dataset.notice
      return raw ? JSON.parse(raw) as { title: string, description: string, to?: unknown } : null
    }
    return { section, notice }
  }

  it('shows the generated suggestions without a notice', () => {
    const { section, notice } = mount({ hash: 'h', items: [{ title: 'A', prompt: 'a' }] })
    expect(section()).not.toBeNull()
    expect(notice()).toBeNull()
  })

  it('guides the owner to set a title model on their profile', () => {
    const { section, notice } = mount({ hash: '', items: [], reason: 'title_model_missing' })
    expect(section()).not.toBeNull()
    expect(notice()).toEqual({
      title: 'chatExamples.titleModelMissing.title',
      description: 'chatExamples.titleModelMissing.description',
      to: { name: 'profile' },
    })
  })

  it('tells anyone else to ask the owner, with nowhere to go', () => {
    const { section, notice } = mount({ hash: '', items: [], reason: 'owner_title_model_missing' })
    expect(section()).not.toBeNull()
    expect(notice()).toEqual({
      title: 'chatExamples.ownerTitleModelMissing.title',
      description: 'chatExamples.ownerTitleModelMissing.description',
    })
  })

  it('hides refresh while the notice shows, since it cannot generate either', () => {
    const { section } = mount({ hash: '', items: [], reason: 'title_model_missing' })
    expect(section()).not.toBeNull()
    expect(root!.textContent).not.toContain('chatExamples.refresh')
  })

  it('stays hidden when generation failed', () => {
    const { section } = mount({ hash: '', items: [] })
    expect(section()).toBeNull()
  })

  it('stays hidden once dismissed, notice included', () => {
    state.dismissed.value = true
    const { section } = mount({ hash: '', items: [], reason: 'title_model_missing' })
    expect(section()).toBeNull()
  })
})
