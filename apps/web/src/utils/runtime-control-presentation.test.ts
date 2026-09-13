import { expect, it } from 'vitest'
import { localizeRuntimeCommandResult } from './runtime-control-presentation'

it('仅摘要命令字段，原生 schema 和新增字段保留在 JSON 详情中', () => {
  const t = (key: string, fallback: string) => key.endsWith('.model') ? '模型' : fallback
  const schema = { properties: { model: { type: 'string' } }, required: ['model'] }
  const data = [{ name: 'server', authStatus: 'oAuth', tools: { query: { inputSchema: schema } } }]
  const result = localizeRuntimeCommandResult({ kind: 'runtime_command', data }, t, 'zh', 'mcp')
  expect(result.text).toBe('- server — oAuth')
  expect(result.data).toBe(data)
  expect(schema).toEqual({ properties: { model: { type: 'string' } }, required: ['model'] })

  const status = { model: 'native-model', future_field: { model: 'opaque' } }
  const observed = localizeRuntimeCommandResult({ kind: 'runtime_command', data: status }, t, 'zh', 'status')
  expect(observed.text).toBe('模型: native-model')
  expect(observed.data).toBe(status)
  expect(localizeRuntimeCommandResult({ kind: 'runtime_command', data: status }, t, 'zh', 'unknown').text)
    .toBe(JSON.stringify(status, null, 2))
})
