import type { ProvidersGetResponse, ProvidertemplatesGetResponse } from '@memohai/sdk'

export interface ProviderConfigField {
  key: string
  type: string
  title: string
  description: string
  required: boolean
  secret: boolean
  example?: unknown
  enum: string[]
  order: number
  constraint?: { min?: number; max?: number; step?: number } | null
}

const CREDENTIAL_FIELD_PRIORITY = [
  'account_id',
  'api_key',
  'api_token',
  'cx',
  'secret_id',
  'secret_key',
] as const

const REGULAR_OPTION_PRIORITY = [
  'search_type',
  'language',
  'safesearch',
  'categories',
] as const

function asRecord(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  return value as Record<string, unknown>
}

function normalizeConstraint(key: string, type: string, raw: unknown): ProviderConfigField['constraint'] {
  const record = asRecord(raw)
  if (record) {
    return {
      min: typeof record.min === 'number' ? record.min : undefined,
      max: typeof record.max === 'number' ? record.max : undefined,
      step: typeof record.step === 'number' ? record.step : undefined,
    }
  }
  if (type === 'number' && (key === 'timeout_seconds' || key.endsWith('_seconds'))) {
    return { min: 1 }
  }
  return undefined
}

// Map-backed Go schemas serialize keys alphabetically, so pin a deterministic
// UX order when the schema omits explicit `order`: credentials first, base_url
// second, regular options in the middle, and headers + timeout_seconds last.
function defaultFieldOrder(key: string, secret: boolean): number {
  const credIndex = CREDENTIAL_FIELD_PRIORITY.indexOf(key as (typeof CREDENTIAL_FIELD_PRIORITY)[number])
  if (credIndex !== -1) return credIndex
  if (secret) return CREDENTIAL_FIELD_PRIORITY.length
  if (key === 'base_url') return 100
  if (key === 'headers') return 800
  if (key === 'timeout_seconds' || key.endsWith('_seconds')) return 900
  const optionIndex = REGULAR_OPTION_PRIORITY.indexOf(key as (typeof REGULAR_OPTION_PRIORITY)[number])
  if (optionIndex !== -1) return 200 + optionIndex
  return 300
}

function normalizeField(key: string, value: unknown, fallbackOrder: number): ProviderConfigField {
  const field = asRecord(value) ?? {}
  const rawType = typeof field.type === 'string' ? field.type : 'string'
  const enumValues = Array.isArray(field.enum)
    ? field.enum.filter((item): item is string => typeof item === 'string')
    : []
  const secret = field.secret === true || rawType === 'secret' || rawType === 'password'
  const type = secret
    ? 'secret'
    : (enumValues.length > 0 && rawType === 'string' ? 'enum' : rawType)
  return {
    key,
    type,
    title: typeof field.title === 'string' ? field.title : key,
    description: typeof field.description === 'string' ? field.description : '',
    required: field.required === true,
    secret,
    example: field.example,
    enum: enumValues,
    order: typeof field.order === 'number' ? field.order : fallbackOrder,
    constraint: normalizeConstraint(key, type, field.constraint),
  }
}

export function normalizeProviderConfigFields(schema: unknown): ProviderConfigField[] {
  const fields = asRecord(schema)?.fields
  if (Array.isArray(fields)) {
    return fields
      .map((field, index) => {
        const record = asRecord(field)
        const key = typeof record?.key === 'string' ? record.key : ''
        return key ? normalizeField(key, record, index) : null
      })
      .filter((field): field is ProviderConfigField => field !== null)
      .sort((a, b) => a.order - b.order)
  }

  const record = asRecord(fields)
  if (!record) return []
  return Object.entries(record)
    .map(([key, field]) => {
      const raw = asRecord(field) ?? {}
      const rawType = typeof raw.type === 'string' ? raw.type : 'string'
      const secret = raw.secret === true || rawType === 'secret' || rawType === 'password'
      return normalizeField(key, field, defaultFieldOrder(key, secret))
    })
    .sort((a, b) => a.order - b.order || a.key.localeCompare(b.key))
}

export function templateConfigFields(template?: ProvidertemplatesGetResponse | null): ProviderConfigField[] {
  return normalizeProviderConfigFields(template?.config_schema)
}

export function templateDefaultConfig(template?: ProvidertemplatesGetResponse | null): Record<string, unknown> {
  return { ...((asRecord(template?.default_config) ?? {})) }
}

export function providerConfigDefaults(schema: unknown): Record<string, unknown> {
  return Object.fromEntries(normalizeProviderConfigFields(schema)
    .filter(field => !field.secret && !field.required && field.example !== undefined)
    .map(field => [field.key, field.example]))
}

export function providerDraftFromTemplate(template: ProvidertemplatesGetResponse): ProvidersGetResponse {
  return {
    provider_template_id: template.id,
    name: template.name ?? template.key ?? '',
    client_type: template.driver ?? '',
    icon: template.icon,
    enable: false,
    config: templateDefaultConfig(template),
    metadata: { ...((asRecord(template.metadata) ?? {})) },
  }
}

export function isTemplateConfigured(template: ProvidertemplatesGetResponse): boolean {
  if (template.configured === true) return true
  return asRecord(template.metadata)?.configured === true
}
