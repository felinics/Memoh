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
    findMissingRequiredManagedField: (_profile: unknown, managed: Record<string, unknown>, setupMode: string) =>
      setupMode === 'self' || String(managed.api_key ?? '').trim() ? null : { id: 'api_key' },
    isACPAgentEnabled: (metadata: Record<string, unknown> | undefined, provider: string) => {
      const acp = metadata?.acp as { agents?: Record<string, { enabled?: boolean }> } | undefined
      return acp?.agents?.[provider]?.enabled === true
    },
    normalizeACPAgentID: (value: unknown) => String(value ?? '').trim().toLowerCase(),
    readACPAgentConfig: (metadata: Record<string, unknown> | undefined, provider: string) => {
      const acp = metadata?.acp as { agents?: Record<string, { setup_mode?: string, managed?: Record<string, unknown> }> } | undefined
      const config = acp?.agents?.[provider] ?? {}
      return { setupMode: config.setup_mode ?? 'api_key', setupModeSet: !!config.setup_mode, managed: config.managed ?? {} }
    },
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
  { id: 'agent-custom', name: 'Custom', runtime: 'acp', enabled: true, metadata: { provider: 'custom-agent' } },
]

const acpProfiles = [
  { id: 'custom-agent', display_name: 'Custom' },
]

const configuredMetadata = {
  acp: {
    agents: {
      'custom-agent': { enabled: true, setup_mode: 'api_key', managed: { api_key: 'custom-key' } },
    },
  },
}

async function mountCard(form: ReturnType<typeof createForm>, options: {
  botAgents?: typeof botAgents
	botMetadata?: Record<string, unknown>
} = {}) {
  const Card = (await import('./settings-interaction-card.vue')).default
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(Card, {
    form,
    models: [],
    providers: [],
    botAgents: options.botAgents ?? botAgents,
		botMetadata: options.botMetadata ?? configuredMetadata,
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
				botAgents[2],
			],
			botMetadata: {
				acp: {
					agents: {
            'custom-agent': { enabled: true, setup_mode: 'api_key', managed: {} },
					},
				},
			},
		})

		expect(root.querySelector('[data-option-value="agent:agent-codex"]')).toBeNull()
		expect(root.querySelector('[data-option-value="agent:agent-custom"]')).toBeNull()
		expect(root.querySelector('[data-option-value="agent:agent-claude"]')).not.toBeNull()

    app.unmount()
  })

  it('keeps legacy enabled Agents selectable when setup mode was not stored', async () => {
    const form = createForm()
    const { app, root } = await mountCard(form, {
      botAgents: [
        { ...botAgents[0], agent_credential_id: undefined },
        botAgents[2],
      ],
      botMetadata: {
        acp: {
          agents: {
            'custom-agent': { enabled: true, managed: {} },
          },
        },
      },
    })

    expect(root.querySelector('[data-option-value="agent:agent-custom"]')).not.toBeNull()
    expect(root.querySelector('[data-option-value="agent:agent-codex"]')).toBeNull()

    app.unmount()
  })
})
