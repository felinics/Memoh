import { describe, expect, it } from 'vitest'
import {
  maskApiKey,
  parseProviderConnectPayload,
  PayloadError,
} from './provider-connect'

function encode(obj: unknown): string {
  const b64 = btoa(JSON.stringify(obj))
  return b64.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

const valid = {
  v: 1,
  kind: 'memoh-provider-import',
  name: 'My Gateway',
  base_url: 'https://api.example.com/v1',
  api_key: 'sk-abcdef123456',
  client_type: 'openai-completions',
  template: 'newapi',
}

describe('parseProviderConnectPayload', () => {
  it('parses a full payload', () => {
    expect(parseProviderConnectPayload(encode(valid))).toEqual({
      name: 'My Gateway',
      baseUrl: 'https://api.example.com/v1',
      apiKey: 'sk-abcdef123456',
      clientType: 'openai-completions',
      template: 'newapi',
    })
  })

  it('defaults client_type and derives name from host', () => {
    const { name: _n, client_type: _c, template: _t, ...rest } = valid
    const payload = parseProviderConnectPayload(encode({ ...rest, base_url: 'https://gw.example.com/v1/' }))
    expect(payload.clientType).toBe('openai-completions')
    expect(payload.name).toBe('gw.example.com')
    expect(payload.baseUrl).toBe('https://gw.example.com/v1')
    expect(payload.template).toBeUndefined()
  })

  it('rejects bad input with precise reasons', () => {
    expect(() => parseProviderConnectPayload('')).toThrow(new PayloadError('missing'))
    expect(() => parseProviderConnectPayload('!!!')).toThrow(new PayloadError('decode'))
    expect(() => parseProviderConnectPayload(encode({ ...valid, kind: 'other' }))).toThrow(new PayloadError('kind'))
    expect(() => parseProviderConnectPayload(encode({ ...valid, v: 2 }))).toThrow(new PayloadError('version'))
    expect(() => parseProviderConnectPayload(encode({ ...valid, base_url: '' }))).toThrow(new PayloadError('baseUrl'))
    expect(() => parseProviderConnectPayload(encode({ ...valid, base_url: 'not a url' }))).toThrow(new PayloadError('baseUrl'))
    expect(() => parseProviderConnectPayload(encode({ ...valid, base_url: 'ftp://x.com' }))).toThrow(new PayloadError('baseUrl'))
    expect(() => parseProviderConnectPayload(encode({ ...valid, api_key: ' ' }))).toThrow(new PayloadError('apiKey'))
    expect(() => parseProviderConnectPayload(encode({ ...valid, client_type: 'github-copilot' }))).toThrow(new PayloadError('clientType'))
  })
})

describe('maskApiKey', () => {
  it('masks all but the last 4 characters', () => {
    expect(maskApiKey('sk-abcdef123456')).toBe('••••3456')
    expect(maskApiKey('abcd')).toBe('••••')
  })
})
