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
    isACPAgentConfigured: (agent: { metadata?: { managed?: Record<string, unknown> } }, profile: unknown) =>
      !!profile && !!String(agent.metadata?.managed?.api_key ?? '').trim(),
  }
})

vi.mock('@/utils/bot-agent', async (importActual) => {
  const actual = await importActual<typeof import('@/utils/bot-agent')>()
  const { defineComponent, h } = await import('vue')
  return {
    ...actual,
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
  { id: 'agent-codex', name: 'Codex', runtime: 'codex', enabled: true, agent_credential_id: 'credential-codex', metadata: { provider: 'codex', auth: 'api_key' } },
  { id: 'agent-claude', name: 'Claude Code', runtime: 'claude-code', enabled: true, metadata: { provider: 'claude-code', auth: 'workspace' } },
  { id: 'agent-custom', name: 'Custom', runtime: 'acp', enabled: true, metadata: { provider: 'custom-agent', managed: { api_key: 'custom-key' } } },
]

const acpProfiles = [
  { id: 'custom-agent', display_name: 'Custom' },
]

async function mountCard(form: ReturnType<typeof createForm>, options: {
  botAgents?: Array<Record<string, unknown>>
} = {}) {
  const Card = (await import('./settings-interaction-card.vue')).default
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(Card, {
    form,
    models: [],
    providers: [],
    botAgents: options.botAgents ?? botAgents,
		acpProfiles,
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

    expect(form.chat_runtime).toBe('codex')
    expect(form.default_bot_agent_id).toBe('agent-codex')
    expect(form.chat_acp_agent_id).toBe('')
    expect(form.chat_acp_project_path).toBe('/data')
    expect(form.chat_acp_project_mode).toBe('project')

    app.unmount()
  })

  it('switches back to Memoh and clears the default Bot Agent binding', async () => {
    const form = createForm({
      chat_runtime: 'codex',
      default_bot_agent_id: 'agent-codex',
      chat_acp_agent_id: '',
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

	it('hides Agents whose provider setup is incomplete', async () => {
		const form = createForm()
		const { app, root } = await mountCard(form, {
			botAgents: [
				{ ...botAgents[0], agent_credential_id: undefined },
				botAgents[1],
				{ ...botAgents[2], metadata: { provider: 'custom-agent', managed: {} } },
			],
		})

		expect(root.querySelector('[data-option-value="agent:agent-codex"]')).toBeNull()
		expect(root.querySelector('[data-option-value="agent:agent-custom"]')).toBeNull()
		expect(root.querySelector('[data-option-value="agent:agent-claude"]')).not.toBeNull()

    app.unmount()
  })

  // Every custom ACP Agent shares one provider; each is judged by its own setup.
  it('judges two Agents of the same provider by their own setup', async () => {
    const form = createForm()
    const { app, root } = await mountCard(form, {
      botAgents: [
        { id: 'agent-hermes', name: 'Hermes', runtime: 'acp', enabled: true, metadata: { provider: 'custom-agent', managed: { api_key: 'hermes-key' } } },
        { id: 'agent-grok', name: 'Grok', runtime: 'acp', enabled: true, metadata: { provider: 'custom-agent', managed: {} } },
      ],
    })

    expect(root.querySelector('[data-option-value="agent:agent-hermes"]')).not.toBeNull()
    expect(root.querySelector('[data-option-value="agent:agent-grok"]')).toBeNull()

    app.unmount()
  })
})
