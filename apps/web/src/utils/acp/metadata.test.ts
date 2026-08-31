import { describe, expect, it } from 'vitest'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import {
  ensureACPAgentForm,
  findMissingRequiredManagedField,
  isACPAgentEnabled,
  normalizeACPForm,
  readACPAgentConfig,
  readACPConfig,
  withACPMetadata,
  withEnabledACPAgentMetadataIfConfigured,
  type ACPForm,
} from './metadata'

const codexProfile: AcpprofilePublicProfile = {
  id: 'codex',
  display_name: 'Codex',
  setup_modes: ['api_key', 'oauth', 'self'],
  managed_fields: [
    {
      id: 'api_key',
      label: 'OpenAI API key',
      type: 'password',
      required: true,
      sensitive: true,
    },
    {
      id: 'base_url',
      label: 'OpenAI base URL',
      type: 'url',
    },
  ],
}

const claudeCodeProfile: AcpprofilePublicProfile = {
  id: 'claude-code',
  display_name: 'Claude Code',
  setup_modes: ['api_key', 'oauth', 'self'],
  managed_fields: [
    {
      id: 'api_key',
      label: 'Anthropic API key',
      type: 'password',
      required: true,
      sensitive: true,
    },
    {
      id: 'base_url',
      label: 'Anthropic base URL',
      type: 'url',
    },
    {
      id: 'oauth_token',
      label: 'Claude Code OAuth token',
      type: 'password',
      required: true,
      sensitive: true,
    },
  ],
}

describe('acp-metadata', () => {
  it('builds ACP form state from profile schema and metadata', () => {
    const metadata = {
      acp: {
        agents: {
          codex: {
            enabled: true,
          },
        },
      },
    }

    expect(isACPAgentEnabled(metadata, 'Codex')).toBe(true)
    expect(readACPConfig(metadata, [codexProfile])).toEqual({
      agents: {
        codex: {
          enabled: true,
          setup_mode: 'api_key',
          managed: {
            api_key: '',
            base_url: '',
          },
        },
      },
    })
  })

  it('keeps only schema-managed fields and normalizes enabled agent form', () => {
    const form = readACPConfig({
      acp: {
        agents: {
          codex: {
            enabled: true,
            setup_mode: 'api_key',
            managed: {
              api_key: 'sk-...cret',
              base_url: 'https://api.example.test/v1',
              extra: 'ignored',
            },
          },
        },
      },
    }, [codexProfile])

    expect(normalizeACPForm(form, [codexProfile])).toEqual({
      agents: {
        codex: {
          enabled: true,
          setup_mode: 'api_key',
          managed: {
            api_key: 'sk-...cret',
            base_url: 'https://api.example.test/v1',
          },
        },
      },
    })
  })

  it('initializes missing agent form entries from profile schema', () => {
    const form: ACPForm = { agents: {} }

    const agent = ensureACPAgentForm(form, codexProfile)
    agent.enabled = true
    agent.managed.api_key = 'sk-test'

    expect(form.agents.codex).toEqual({
      enabled: true,
      setup_mode: 'api_key',
      managed: {
        api_key: 'sk-test',
        base_url: '',
      },
    })
  })

  it('validates Codex setup mode required fields', () => {
    expect(findMissingRequiredManagedField(codexProfile, {}, 'oauth')?.id).toBe('api_key')
    expect(findMissingRequiredManagedField(codexProfile, {
      api_key: '',
    }, 'api_key')?.id).toBe('api_key')
  })

  it('marks missing setup_mode as legacy when reading agent config', () => {
    const legacy = readACPAgentConfig({
      acp: {
        agents: {
          codex: {
            enabled: true,
            managed: {},
          },
        },
      },
    }, 'codex')
    expect(legacy.setupMode).toBe('api_key')
    expect(legacy.setupModeSet).toBe(false)

    const explicit = readACPAgentConfig({
      acp: {
        agents: {
          codex: {
            enabled: true,
            setup_mode: 'api_key',
            managed: {},
          },
        },
      },
    }, 'codex')
    expect(explicit.setupMode).toBe('api_key')
    expect(explicit.setupModeSet).toBe(true)
    expect(findMissingRequiredManagedField(codexProfile, explicit.managed, explicit.setupMode)?.id).toBe('api_key')
  })

  it('writes ACP metadata into the agents map', () => {
    const next = withACPMetadata({
      workspace: { backend: 'docker' },
    }, {
      agents: {
        codex: {
          enabled: true,
          setup_mode: 'self',
          managed: {
            api_key: '',
            base_url: '',
          },
        },
      },
    })

    expect(next).toEqual({
      workspace: { backend: 'docker' },
      acp: {
        agents: {
          codex: {
            enabled: true,
            setup_mode: 'self',
            managed: {
              api_key: '',
              base_url: '',
            },
          },
        },
      },
    })
  })

  it('serializes cleared sensitive managed fields as null for backend three-state PUT', () => {
    const next = withACPMetadata({
      acp: {
        agents: {
          codex: {
            enabled: true,
            setup_mode: 'api_key',
            managed: {
              api_key: 'sk-...cret',
              base_url: 'https://api.example.test/v1',
            },
          },
        },
      },
    }, {
      agents: {
        codex: {
          enabled: false,
          setup_mode: 'self',
          managed: {
            api_key: '',
            base_url: '',
          },
        },
      },
    }, [codexProfile])

    expect(next).toEqual({
      acp: {
        agents: {
          codex: {
            enabled: false,
            setup_mode: 'self',
            managed: {
              api_key: null,
              base_url: '',
            },
          },
        },
      },
    })
  })

  it('preserves masked sensitive managed fields when switching setup modes', () => {
    const next = withACPMetadata({
      acp: {
        agents: {
          codex: {
            enabled: true,
            setup_mode: 'api_key',
            managed: {
              api_key: 'sk-...cret',
              base_url: 'https://api.example.test/v1',
            },
          },
        },
      },
    }, {
      agents: {
        codex: {
          enabled: true,
          setup_mode: 'self',
          managed: {
            api_key: 'sk-...cret',
            base_url: 'https://api.example.test/v1',
          },
        },
      },
    }, [codexProfile])

    expect(next).toEqual({
      acp: {
        agents: {
          codex: {
            enabled: true,
            setup_mode: 'self',
            managed: {
              api_key: 'sk-...cret',
              base_url: 'https://api.example.test/v1',
            },
          },
        },
      },
    })
  })

  it('reads one agent config for ACP session creation validation', () => {
    const config = readACPAgentConfig({
      acp: {
        agents: {
          codex: {
            setup_mode: 'api_key',
            managed: {
              api_key: 'sk-...cret',
            },
          },
        },
      },
    }, 'CODEX')

    expect(config).toEqual({
      setupMode: 'api_key',
      setupModeSet: true,
      managed: {
        api_key: 'sk-...cret',
      },
    })
  })
})

describe('withEnabledACPAgentMetadataIfConfigured', () => {
  it('does not write incomplete profile defaults during Agent creation', () => {
    expect(withEnabledACPAgentMetadataIfConfigured({}, {
      id: 'acp',
      display_name: 'ACP',
      setup_modes: ['api_key'],
      managed_fields: [{ id: 'command', required: true }],
    })).toBeUndefined()
  })

  it('does not materialize an empty Claude Code OAuth setup during Agent creation', () => {
    expect(withEnabledACPAgentMetadataIfConfigured({}, claudeCodeProfile)).toBeUndefined()
  })

  it('enables only a configured selected profile and preserves unrelated ACP config', () => {
    const codex = {
      enabled: true,
      setup_mode: 'oauth',
      managed: {},
    }
    const metadata = {
      acp: {
        agents: {
          codex,
          acp: {
            enabled: false,
            setup_mode: 'api_key',
            managed: { command: 'custom-acp', arguments: '' },
          },
        },
      },
    }
    const next = withEnabledACPAgentMetadataIfConfigured(metadata, {
      id: 'acp',
      display_name: 'ACP',
      setup_modes: ['api_key'],
      managed_fields: [
        { id: 'command', required: true },
        { id: 'arguments' },
      ],
    })
    const agents = (next?.acp as { agents: Record<string, unknown> }).agents

    expect(agents.codex).toBe(codex)
    expect(agents.acp).toEqual({
      enabled: true,
      setup_mode: 'api_key',
      managed: { command: 'custom-acp', arguments: '' },
    })
  })
})
