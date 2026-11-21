// Failure Modes Covered:
// 1. Unordered Go map[string]Field schemas serialize alphabetically (`base_url`,
//    `categories`, `headers`, `timeout_seconds`, ...), which without deterministic
//    priority puts `headers` or `timeout_seconds` above credentials or `base_url`.
// 2. `required` fields that carry an `example` (e.g. SearXNG `base_url: "http://localhost:8080"`)
//    or `secret` fields carrying an `example` must NOT be prefilled by `providerConfigDefaults`
//    (their `example` is a placeholder hint, not a submitted default), whereas optional
//    non-secret fields (`timeout_seconds`, `safesearch`) must hydrate defaults.
// 3. `timeout_seconds` / `*_seconds` number fields without explicit server constraints
//    would allow `0` or negative values unless normalized to `{ min: 1 }`, while
//    explicit server constraints (`min`, `max`, `step`) must be preserved.
// 4. Go schemas represent enum selects as `{ type: "string", enum: [...] }`; failing
//    to normalize non-secret string+enum to `type: "enum"` renders a plain text input.

import { describe, expect, it } from 'vitest'
import {
  normalizeProviderConfigFields,
  providerConfigDefaults,
} from './provider-template'

describe('normalizeProviderConfigFields & providerConfigDefaults', () => {
  it('orders unordered map-backed fields deterministically: credentials -> base_url -> regular options -> custom -> headers -> *_seconds', () => {
    // Deliberately feed keys in reverse/scrambled order relative to desired UX order.
    const schema = {
      fields: {
        timeout_seconds: { type: 'number', title: 'Timeout', example: 15 },
        headers: { type: 'object', title: 'HTTP headers' },
        custom_region: { type: 'string', title: 'Region' },
        categories: { type: 'string', title: 'Categories', example: 'general' },
        safesearch: { type: 'string', enum: ['off', 'moderate', 'strict'], example: 'moderate' },
        language: { type: 'string', title: 'Language', example: 'en' },
        search_type: { type: 'string', enum: ['search', 'news'], example: 'search' },
        base_url: { type: 'string', required: true, example: 'http://localhost:8080' },
        custom_token: { type: 'string', secret: true },
        secret_key: { type: 'secret' },
        secret_id: { type: 'string', secret: true },
        cx: { type: 'string' },
        api_token: { type: 'password' },
        api_key: { type: 'secret' },
        account_id: { type: 'string' },
      },
    }

    const fields = normalizeProviderConfigFields(schema)
    expect(fields.map(field => field.key)).toEqual([
      'account_id',
      'api_key',
      'api_token',
      'cx',
      'secret_id',
      'secret_key',
      'custom_token',
      'base_url',
      'search_type',
      'language',
      'safesearch',
      'categories',
      'custom_region',
      'headers',
      'timeout_seconds',
    ])
  })

  it('honors explicit field `order` when provided by array or map schemas', () => {
    const schema = {
      fields: {
        second: { type: 'string', order: 20 },
        first: { type: 'string', order: 10 },
      },
    }
    expect(normalizeProviderConfigFields(schema).map(field => field.key)).toEqual(['first', 'second'])
  })

  it('excludes `required` and `secret` fields from providerConfigDefaults while hydrating optional non-secret examples', () => {
    const searxngLikeSchema = {
      fields: {
        api_key: { type: 'string', secret: true, example: 'placeholder-secret' },
        base_url: { type: 'string', required: true, example: 'http://localhost:8080' },
        categories: { type: 'string', required: false, example: 'general' },
        safesearch: { type: 'string', enum: ['0', '1', '2'], example: '1' },
        timeout_seconds: { type: 'number', example: 15 },
        headers: { type: 'object' },
      },
    }

    const defaults = providerConfigDefaults(searxngLikeSchema)
    expect(defaults).toEqual({
      categories: 'general',
      safesearch: '1',
      timeout_seconds: 15,
    })
    expect(defaults).not.toHaveProperty('base_url')
    expect(defaults).not.toHaveProperty('api_key')
    expect(defaults).not.toHaveProperty('headers')
  })

  it('adds `{ min: 1 }` constraint to `timeout_seconds` and `*_seconds` number fields without overriding explicit constraints', () => {
    const fields = normalizeProviderConfigFields({
      fields: {
        timeout_seconds: { type: 'number', example: 10 },
        poll_interval_seconds: { type: 'number' },
        custom_limit: { type: 'number' },
        bounded_seconds: { type: 'number', constraint: { min: 5, max: 60, step: 5 } },
      },
    })

    const byKey = Object.fromEntries(fields.map(field => [field.key, field]))
    expect(byKey.timeout_seconds?.constraint).toEqual({ min: 1 })
    expect(byKey.poll_interval_seconds?.constraint).toEqual({ min: 1 })
    expect(byKey.custom_limit?.constraint).toBeUndefined()
    expect(byKey.bounded_seconds?.constraint).toEqual({ min: 5, max: 60, step: 5 })
  })

  it('normalizes non-secret string fields with `enum` options to `type: "enum"`, while keeping secret fields as `secret`', () => {
    const fields = normalizeProviderConfigFields({
      fields: {
        safesearch: { type: 'string', enum: ['off', 'moderate', 'strict'] },
        secret_with_enum: { type: 'string', secret: true, enum: ['a', 'b'] },
      },
    })

    const byKey = Object.fromEntries(fields.map(field => [field.key, field]))
    expect(byKey.safesearch?.type).toBe('enum')
    expect(byKey.safesearch?.enum).toEqual(['off', 'moderate', 'strict'])
    expect(byKey.secret_with_enum?.type).toBe('secret')
    expect(byKey.secret_with_enum?.secret).toBe(true)
  })
})
