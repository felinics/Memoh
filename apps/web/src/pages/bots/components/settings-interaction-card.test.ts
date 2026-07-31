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
  const { defineComponent, h } = await import('vue')
  const normalizeACPAgentID = (value: unknown) => typeof value === 'string' ? value.trim().toLowerCase() : ''
  return {
    ACP_DEFAULT_PROJECT_MODE: 'project',
    ACP_DEFAULT_PROJECT_PATH: '/data',
    acpAgentIcon: () => defineComponent({ setup: () => () => h('span') }),
    findMissingRequiredManagedField: () => null,
    isACPAgentEnabled: (metadata: Record<string, unknown> | undefined, agentID: unknown) => {
      const agents = ((metadata?.acp as { agents?: Record<string, { enabled?: boolean }> } | undefined)?.agents) ?? {}
      return agents[normalizeACPAgentID(agentID)]?.enabled === true
    },
    normalizeACPAgentID,
    readACPAgentConfig: () => ({ setupMode: 'api_key', setupModeSet: false, managed: {} }),
  }
})

function createForm(overrides: Record<string, unknown> = {}) {
  return reactive({
    chat_model_id: '',
    chat_runtime: 'model',
    chat_acp_agent_id: '',
    chat_acp_project_path: '',
    chat_acp_project_mode: '',
    reasoning_enabled: false,
    reasoning_effort: 'medium',
    show_tool_calls_in_im: false,
    ...overrides,
  })
}

const profiles = [
  { id: 'codex', display_name: 'Codex' },
  { id: 'claude-code', display_name: 'Claude Code' },
]

const metadata = {
  acp: {
    agents: {
      codex: { enabled: true },
      'claude-code': { enabled: true },
    },
  },
}

async function mountCard(form: ReturnType<typeof createForm>, options: {
  acpProfiles?: typeof profiles
  botMetadata?: typeof metadata
} = {}) {
  const Card = (await import('./settings-interaction-card.vue')).default
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(Card, {
    form,
    models: [],
    providers: [],
    acpProfiles: options.acpProfiles ?? profiles,
    botMetadata: options.botMetadata ?? metadata,
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

  it('selects an ACP Agent and initializes its project defaults', async () => {
    const form = createForm()
    const { app, root } = await mountCard(form)

    const selector = root.querySelector('[data-select-value="memoh"]')
    expect(selector).not.toBeNull()
    expect(root.querySelector('[data-option-value="acp:codex"]')).not.toBeNull()

    uiState.nextSelectValue = 'acp:codex'
    selector!.dispatchEvent(new MouseEvent('click'))
    await nextTick()

    expect(form.chat_runtime).toBe('acp_agent')
    expect(form.chat_acp_agent_id).toBe('codex')
    expect(form.chat_acp_project_path).toBe('/data')
    expect(form.chat_acp_project_mode).toBe('project')

    app.unmount()
  })

  it('switches back to Memoh without discarding the saved ACP Agent', async () => {
    const form = createForm({
      chat_runtime: 'acp_agent',
      chat_acp_agent_id: 'codex',
      chat_acp_project_path: '/data/project',
      chat_acp_project_mode: 'project',
    })
    const { app, root } = await mountCard(form)

    const selector = root.querySelector('[data-select-value="acp:codex"]')
    expect(selector).not.toBeNull()

    uiState.nextSelectValue = 'memoh'
    selector!.dispatchEvent(new MouseEvent('click'))
    await nextTick()

    expect(form.chat_runtime).toBe('model')
    expect(form.chat_acp_agent_id).toBe('codex')
    expect(form.chat_acp_project_path).toBe('/data/project')

    app.unmount()
  })

  it('shows a recoverable warning when the saved ACP Agent is unavailable', async () => {
    const form = createForm({
      chat_runtime: 'acp_agent',
      chat_acp_agent_id: 'removed-agent',
    })
    const { app, root } = await mountCard(form)

    expect(root.textContent).toContain('bots.settings.defaultAgentUnavailable')
    expect(root.textContent).toContain('bots.settings.defaultAgentUnavailableDescription')
    expect(root.querySelector('[data-option-value="memoh"]')).not.toBeNull()

    app.unmount()
  })
})
