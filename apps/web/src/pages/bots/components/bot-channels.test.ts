// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, ref } from 'vue'

const state = vi.hoisted(() => ({ settings: null as unknown, push: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: { tab: 'channels' } }), useRouter: () => ({ push: state.push }) }))
vi.mock('@pinia/colada', () => ({ useQuery: (options: { key: () => string[] }) => ({
  data: options.key()[0] === 'bot-settings' ? state.settings : ref([]), isLoading: ref(false), refetch: vi.fn(),
}) }))
vi.mock('@felinic/ui', async () => {
  const { defineComponent, h } = await import('vue')
  const shell = defineComponent({
    setup: (_, { slots }) => () => h('div', [slots.actions?.(), slots.default?.()]),
  })
  const banner = defineComponent({
    props: {
      title: { type: String, default: '' },
      description: { type: String, default: '' },
    },
    emits: ['click'],
    setup: (props, { emit }) => () => h('button', {
      'data-notice': '', onClick: () => emit('click'),
    }, [props.title, props.description]),
  })
  return Object.fromEntries([
    'SettingsSection', 'Button', 'Skeleton', 'Dialog', 'DialogContent', 'DialogHeader', 'DialogTitle',
    'Empty', 'EmptyTitle', 'EmptyDescription', 'EmptyContent', 'BackendCard', 'PageShell', 'SwapTransition', 'CalloutBanner',
  ].map(name => [name, name === 'CalloutBanner' ? banner : shell]))
})
vi.mock('./channel-settings-panel.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/channel-icon/index.vue', () => ({ default: { template: '<span />' } }))
import BotChannels from './bot-channels.vue'

let unmount = () => {}
afterEach(() => {
  unmount()
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

async function render(settings: unknown) {
  state.settings = ref(settings)
  const target = document.createElement('div')
  document.body.append(target)
  const app = createApp(BotChannels, { botId: 'bot-1' })
  app.mount(target)
  unmount = () => app.unmount()
  await nextTick()
  return target
}

describe('channel model setup guidance', () => {
  it('takes an unconfigured native bot to its default model settings', async () => {
    const target = await render({ chat_runtime: 'model', chat_model_id: '' })
    const notice = target.querySelector<HTMLButtonElement>('[data-notice]')
    expect(notice?.textContent).toContain('bots.channels.modelRequiredDescription')
    notice!.click()
    expect(state.push).toHaveBeenCalledWith({ query: { tab: 'general', section: 'interaction' } })
  })
  it.each([undefined, { chat_runtime: 'model', chat_model_id: 'model-1' }, { chat_runtime: 'codex' }, { chat_runtime: 'acp_agent' }, { chat_runtime: 'claude-code' }])('does not warn for loading, configured or external runtimes: %j', async (settings) => {
    const target = await render(settings)
    expect(target.querySelector('[data-notice]')).toBeNull()
  })
  it('clears the reminder when saved settings arrive', async () => {
    const target = await render({ chat_model_id: '' })
    expect(target.querySelector('[data-notice]')).not.toBeNull()
    const settings = state.settings as ReturnType<typeof ref>
    settings.value = { chat_model_id: 'model-1' }
    await nextTick()
    expect(target.querySelector('[data-notice]')).toBeNull()
  })
})
