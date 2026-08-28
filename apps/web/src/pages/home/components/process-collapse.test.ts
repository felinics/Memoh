import { describe, expect, it } from 'vitest'
import {
  getCollapseOpen,
  groupCollapseKey,
  reasoningCollapseKey,
  setCollapseOpen,
  toolCollapseKey,
} from './process-collapse'

describe('process collapse identity', () => {
  it('distinguishes an explicit close from an unseen block', () => {
    const key = 'tool/message-a/1'

    expect(getCollapseOpen(key)).toBeUndefined()
    setCollapseOpen(key, false)
    expect(getCollapseOpen(key)).toBe(false)
  })

  it('keeps a reasoning key stable as its content grows', () => {
    const block = { id: 4, type: 'reasoning', content: 'first token' }
    const initial = reasoningCollapseKey('assistant-message', block)

    block.content = 'first token followed by the complete thought'

    expect(reasoningCollapseKey('assistant-message', block)).toBe(initial)
  })

  it('scopes identical block ids to their assistant message', () => {
    const reasoning = { id: 1, type: 'reasoning', content: 'same thought' }
    const tool = { id: 2, type: 'tool', toolCallId: 'same-call' }

    expect(reasoningCollapseKey('message-a', reasoning))
      .not.toBe(reasoningCollapseKey('message-b', reasoning))
    expect(toolCollapseKey('message-a', tool))
      .not.toBe(toolCollapseKey('message-b', tool))
  })

  it('keeps a process group key stable while its first item streams', () => {
    const first = { id: 6, type: 'reasoning', content: 'partial' }
    const items = [first]
    const initial = groupCollapseKey('assistant-message', items)

    first.content = 'complete reasoning'
    items.push({ id: 7, type: 'tool', content: '' })

    expect(groupCollapseKey('assistant-message', items)).toBe(initial)
  })
})
