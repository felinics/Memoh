// @vitest-environment jsdom
import { createApp, h, nextTick, ref, type Ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'

interface QueryState {
  data: Ref<{ items: { title: string; prompt: string }[] }>
  status: Ref<string>
  refreshing: Ref<boolean>
  error: Ref<{ code: string; request_id: string }>
  refresh: () => Promise<void>
}
const state = vi.hoisted(() => ({} as { query: QueryState; dismissed: Ref<boolean> }))
vi.mock('@/composables/api/useStarterPrompts', () => ({ useStarterPromptsQuery: () => state.query }))
vi.mock('@/components/chat-examples/use-chat-examples-ui', () => ({ useChatExamplesUi: () => ({ welcomeDismissed: state.dismissed }) }))
vi.mock('@/composables/useComposerPrefill', () => ({ useComposerPrefill: () => ({ prefillComposer: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('./starter-prompt-list.vue', () => ({ default: { props: ['rows'], template: '<div data-rows>{{ rows }}</div>' } }))
vi.mock('@felinic/ui', () => ({
  Button: { template: '<button><slot /></button>' },
  TextButton: { template: '<button><slot /></button>' },
  CalloutBanner: {
    props: ['dismissLabel', 'description'], emits: ['dismiss'],
    template: '<aside>{{ description }}<button v-if="dismissLabel" :aria-label="dismissLabel" @click="$emit(\'dismiss\')">Close</button><slot name="details" /></aside>',
  },
}))
import Suggestions from './chat-example-suggestions.vue'

let app: ReturnType<typeof createApp>
let root: HTMLDivElement
afterEach(() => { app?.unmount(); root?.remove() })

it('dismisses only the error, retains retry and prompts, and shows a subsequent failure', async () => {
  state.dismissed = ref(false)
  state.query = {
    data: ref({ items: [{ title: 'Old', prompt: 'Keep this prompt' }] }),
    status: ref('success'), refreshing: ref(false), error: ref({ code: 'internal', request_id: 'first' }),
    refresh: vi.fn(async () => { state.query.error.value = { code: 'internal', request_id: 'second' } }),
  }
  root = document.createElement('div')
  document.body.append(root)
  app = createApp({ render: () => h(Suggestions, { botId: 'bot', composerReplaceable: true }) })
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
  const close = root.querySelector<HTMLButtonElement>('aside button')
  expect(close).not.toBeNull()
  close!.click()
  await nextTick()
  expect(root.querySelector('aside')).toBeNull()
  expect(root.textContent).toContain('Keep this prompt')
  expect(state.dismissed.value).toBe(false)
  const refresh = [...root.querySelectorAll('button')].find(button => button.textContent === 'chatExamples.refresh')!
  refresh.click()
  await nextTick()
  expect(root.querySelector('aside')?.textContent).toContain('second')
})
