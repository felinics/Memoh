import { describe, expect, it } from 'vitest'
import { filterDiscussProbeModels } from './discuss-probe-models'

const provider = { id: 'p1', enable: true }
const toolCalling = { model_id: 'm1', provider_id: 'p1', enable: true, config: { compatibilities: ['tool-call'] } }

describe('filterDiscussProbeModels', () => {
  it('keeps tool-calling models from enabled providers', () => {
    expect(filterDiscussProbeModels([toolCalling], [provider])).toEqual([toolCalling])
  })

  it('drops models without tool calling', () => {
    const noTools = { ...toolCalling, config: { compatibilities: ['vision'] } }
    expect(filterDiscussProbeModels([noTools], [provider])).toEqual([])
  })

  it('drops models with no declared compatibilities', () => {
    expect(filterDiscussProbeModels([{ ...toolCalling, config: null }], [provider])).toEqual([])
    expect(filterDiscussProbeModels([{ model_id: 'm1', provider_id: 'p1' }], [provider])).toEqual([])
  })

  it('drops disabled models and models from disabled providers', () => {
    expect(filterDiscussProbeModels([{ ...toolCalling, enable: false }], [provider])).toEqual([])
    expect(filterDiscussProbeModels([toolCalling], [{ id: 'p1', enable: false }])).toEqual([])
  })

  it('drops models whose provider is unknown or unset', () => {
    expect(filterDiscussProbeModels([toolCalling], [])).toEqual([])
    expect(filterDiscussProbeModels([{ ...toolCalling, provider_id: null }], [provider])).toEqual([])
  })
})
