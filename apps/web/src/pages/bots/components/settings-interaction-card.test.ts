// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive } from 'vue'

const uiState = vi.hoisted(() => ({ nextSelectValue: '' }))

function translate(key: string) {
  return key
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: translate }),
}))

vi.mock('@felinic/ui', async () => {
  const { defineComponent, h } = await import('vue')
  const Passthrough = defineComponent({
    setup(_props, { slots }) {
      return () => h('div', slots.default?.())
    },
  })
  const Select = defineComponent({
    props: {
      modelValue: { type: String, default: '' },
    },
    emits: ['update:modelValue'],
    setup(props, { emit, slots }) {
      return () => h('div', {
        'data-select-value': props.modelValue,
        onClick: () => {
          if (uiState.nextSelectValue) emit('update:modelValue', uiState.nextSelectValue)
        },
      }, slots.default?.())
    },
  })
  const SelectItem = defineComponent({
    props: {
      value: { type: String, required: true },
    },
    setup(props, { slots }) {
      return () => h('div', { 'data-option-value': props.value }, slots.default?.())
    },
  })
  const SettingsSection = defineComponent({
    setup(_props, { slots }) {
      return () => h('section', slots.default?.())
    },
  })
  const SettingsRow = defineComponent({
    props: {
      label: { type: String, default: '' },
      description: { type: String, default: '' },
    },
    setup(props, { slots }) {
      return () => h('div', { 'data-settings-row': props.label }, [
        h('span', props.label),
        h('p', props.description),
        slots.default?.(),
      ])
    },
  })
  return {
    Select,
    SelectContent: Passthrough,
    SelectItem,
    SelectTrigger: Passthrough,
    SelectValue: Passthrough,
    Switch: Passthrough,
    SettingsSection,
    SettingsRow,
  }
})

vi.mock('./model-select.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return {
    default: defineComponent({
      setup(_props, { slots }) {
        return () => h('div', slots.default?.())
      },
    }),
  }
})

vi.mock('@/utils/acp', async () => {
  return {
    ACP_DEFAULT_PROJECT_MODE: 'project',
    ACP_DEFAULT_PROJECT_PATH: '/data',
  }
})

vi.mock('@/utils/bot-agent', async () => {
  const { defineComponent, h } = await import('vue')
  return {
    botAgentIcon: () => defineComponent({ setup: () => () => h('span') }),
    botAgentName: (agent: { name?: string }) => agent.name ?? '',
    botAgentProvider: (agent: { metadata?: { provider?: string } }) => agent.metadata?.provider ?? '',
  }
})

function createForm(overrides: Record<string, unknown> = {}) {
  return reactive({
    chat_model_id: '',
    chat_runtime: 'model',
    chat_acp_agent_id: '',
    chat_acp_project_path: '',
    chat_acp_project_mode: '',
    default_bot_agent_id: '',
    reasoning_enabled: false,
    reasoning_effort: 'medium',
    show_tool_calls_in_im: false,
    ...overrides,
  })
}

const botAgents = [
  { id: 'agent-codex', name: 'Codex', runtime: 'acp', enabled: true, metadata: { provider: 'codex' } },
  { id: 'agent-claude', name: 'Claude Code', runtime: 'acp', enabled: true, metadata: { provider: 'claude-code' } },
]

async function mountCard(form: ReturnType<typeof createForm>, options: {
  botAgents?: typeof botAgents
} = {}) {
  const Card = (await import('./settings-interaction-card.vue')).default
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(Card, {
    form,
    models: [],
    providers: [],
    botAgents: options.botAgents ?? botAgents,
  })
  app.config.globalProperties.$t = translate
  app.mount(root)
  await nextTick()
  return { app, root }
}

describe('settings interaction default Agent selector', () => {
  beforeEach(() => {
    uiState.nextSelectValue = ''
    document.body.innerHTML = ''
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('selects a Bot Agent row and initializes its project defaults', async () => {
    const form = createForm()
    const { app, root } = await mountCard(form)

    const selector = root.querySelector('[data-select-value="memoh"]')
    expect(selector).not.toBeNull()
    expect(root.querySelector('[data-option-value="agent:agent-codex"]')).not.toBeNull()

    uiState.nextSelectValue = 'agent:agent-codex'
    selector!.dispatchEvent(new MouseEvent('click'))
    await nextTick()

    expect(form.chat_runtime).toBe('acp_agent')
    expect(form.default_bot_agent_id).toBe('agent-codex')
    expect(form.chat_acp_agent_id).toBe('codex')
    expect(form.chat_acp_project_path).toBe('/data')
    expect(form.chat_acp_project_mode).toBe('project')

    app.unmount()
  })

  it('switches back to Memoh and clears the default Bot Agent binding', async () => {
    const form = createForm({
      chat_runtime: 'acp_agent',
      default_bot_agent_id: 'agent-codex',
      chat_acp_agent_id: 'codex',
      chat_acp_project_path: '/data/project',
      chat_acp_project_mode: 'project',
    })
    const { app, root } = await mountCard(form)

    const selector = root.querySelector('[data-select-value="agent:agent-codex"]')
    expect(selector).not.toBeNull()

    uiState.nextSelectValue = 'memoh'
    selector!.dispatchEvent(new MouseEvent('click'))
    await nextTick()

    expect(form.chat_runtime).toBe('model')
    expect(form.default_bot_agent_id).toBe('')
    expect(form.chat_acp_agent_id).toBe('')
    expect(form.chat_acp_project_path).toBe('/data/project')

    app.unmount()
  })

  it('shows a recoverable warning when the saved Bot Agent is unavailable', async () => {
    const form = createForm({
      chat_runtime: 'acp_agent',
      default_bot_agent_id: 'removed-agent',
      chat_acp_agent_id: 'removed-agent',
    })
    const { app, root } = await mountCard(form)

    expect(root.textContent).toContain('bots.settings.defaultAgentUnavailable')
    expect(root.textContent).toContain('bots.settings.defaultAgentUnavailableDescription')
    expect(root.querySelector('[data-option-value="memoh"]')).not.toBeNull()

    app.unmount()
  })
})
