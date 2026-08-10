import { describe, expect, it, vi } from 'vitest'
import {
  asRecord,
  cloneRequestedSkills,
  cloneUserInputState,
  createInvocationId,
  isOptimisticTurn,
  mergeApprovalState,
  normalizeForwardRef,
  normalizeReplyRef,
  normalizeRequestedSkills,
  normalizeTimestamp,
  pickRawString,
  pickString,
  serverMessageId,
  skillActivationTextFromRaw,
  sortChatMessages,
  structuredToolResult,
} from './chat-list.normalize'
import type { ChatMessage, ChatUserTurn } from './chat-list'

vi.mock('@/store/user', () => ({
  useUserStore: () => ({ userInfo: { id: 'user-1' } }),
}))

function userTurn(overrides: Partial<ChatUserTurn> = {}): ChatUserTurn {
  return {
    id: 'u1',
    role: 'user',
    text: 'hello',
    timestamp: '2026-07-09T00:00:00.000Z',
    attachments: [],
    isSelf: true,
    ...overrides,
  } as ChatUserTurn
}

describe('normalizeTimestamp', () => {
  it('normalizes a parseable value to ISO', () => {
    expect(normalizeTimestamp('2026-07-09T12:00:00+08:00')).toBe('2026-07-09T04:00:00.000Z')
  })

  it('falls back to now for empty and garbage input', () => {
    expect(() => new Date(normalizeTimestamp('')).toISOString()).not.toThrow()
    expect(() => new Date(normalizeTimestamp('not-a-date')).toISOString()).not.toThrow()
  })
})

describe('normalizeReplyRef / normalizeForwardRef', () => {
  it('trims fields and drops an all-empty reply', () => {
    expect(normalizeReplyRef({ message_id: ' m1 ', sender: '', preview: '', attachments: [] }))
      .toEqual({ message_id: 'm1', sender: '', preview: '', attachments: [] })
    expect(normalizeReplyRef({ message_id: '', sender: ' ', preview: '', attachments: [] })).toBeUndefined()
    expect(normalizeReplyRef(undefined)).toBeUndefined()
  })

  it('keeps a forward ref only when it carries data, and validates date', () => {
    expect(normalizeForwardRef({ message_id: 'm2', sender: 's', date: Number.NaN }))
      .toEqual({ message_id: 'm2', from_user_id: '', from_conversation_id: '', sender: 's', date: undefined })
    expect(normalizeForwardRef({ message_id: '', sender: '' })).toBeUndefined()
  })
})

describe('record pickers', () => {
  it('asRecord guards non-objects', () => {
    expect(asRecord(null)).toEqual({})
    expect(asRecord('x')).toEqual({})
    expect(asRecord({ a: 1 })).toEqual({ a: 1 })
  })

  it('pickString trims and skips blank values; pickRawString keeps raw', () => {
    expect(pickString({ a: '  ', b: ' x ' }, 'a', 'b')).toBe('x')
    expect(pickRawString({ a: '  ', b: 'x' }, 'a', 'b')).toBe('  ')
  })

  it('structuredToolResult prefers structuredContent when non-empty', () => {
    expect(structuredToolResult({ structuredContent: { v: 1 }, other: 2 })).toEqual({ v: 1 })
    expect(structuredToolResult({ structured_content: { v: 2 }, other: 3 })).toEqual({ v: 2 })
    expect(structuredToolResult({ structuredContent: {}, structured_content: { v: 3 } })).toEqual({ v: 3 })
    expect(structuredToolResult({ structuredContent: {}, other: 2 })).toEqual({ structuredContent: {}, other: 2 })
  })
})

describe('skillActivationTextFromRaw', () => {
  const activation = { skills: [{ name: 'review' }] }

  it('strips a matching slash selector and keeps the remainder', () => {
    expect(skillActivationTextFromRaw('/review please check', activation)).toBe('please check')
    expect(skillActivationTextFromRaw('/review@v2 check', activation)).toBe('check')
  })

  it('leaves non-matching and non-slash text alone', () => {
    expect(skillActivationTextFromRaw('/other do', activation)).toBe('')
    expect(skillActivationTextFromRaw('plain text', activation)).toBe('plain text')
    expect(skillActivationTextFromRaw('plain text', undefined)).toBe('plain text')
  })
})

describe('sortChatMessages', () => {
  it('sorts by timestamp then id, without mutating input', () => {
    const items = [
      userTurn({ id: 'b', timestamp: '2026-07-09T00:00:02.000Z' }),
      userTurn({ id: 'a', timestamp: '2026-07-09T00:00:01.000Z' }),
      userTurn({ id: 'c', timestamp: '2026-07-09T00:00:01.000Z' }),
    ] as ChatMessage[]
    const sorted = sortChatMessages(items)
    expect(sorted.map(m => m.id)).toEqual(['a', 'c', 'b'])
    expect(items.map(m => m.id)).toEqual(['b', 'a', 'c'])
  })
})

describe('isOptimisticTurn', () => {
  it('only flags explicitly optimistic turns', () => {
    expect(isOptimisticTurn(userTurn())).toBe(false)
    expect(isOptimisticTurn(userTurn({ __optimistic: true } as Partial<ChatUserTurn>))).toBe(true)
  })

})

describe('mergeApprovalState', () => {
  it('keeps the resolved local state when the incoming twin regressed to pending', () => {
    const resolved = { approval_id: 'ap1', status: 'approved' }
    const stale = { approval_id: 'ap1', status: 'pending' }
    expect(mergeApprovalState(resolved, stale)).toBe(resolved)
  })

  it('otherwise takes the incoming state, and keeps existing when incoming is absent', () => {
    const existing = { approval_id: 'ap1', status: 'pending' }
    const incoming = { approval_id: 'ap1', status: 'approved' }
    expect(mergeApprovalState(existing, incoming)).toBe(incoming)
    expect(mergeApprovalState(existing, undefined)).toBe(existing)
  })
})

describe('cloneUserInputState', () => {
  it('deep-clones questions, options, and answers', () => {
    const input = {
      user_input_id: 'ui1',
      status: 'pending',
      questions: [{ id: 'q1', options: [{ id: 'o1' }] }],
      answers: [{ question_id: 'q1', question: 'Pick', selected: [{ id: 'o1', label: 'One' }] }],
    }
    const clone = cloneUserInputState(input as never)
    expect(clone).toEqual(input)
    expect(clone.questions?.[0]).not.toBe(input.questions[0])
    expect(clone.questions?.[0]?.options?.[0]).not.toBe(input.questions[0]!.options[0])
    expect(clone.answers?.[0]).not.toBe(input.answers[0])
    expect(clone.answers?.[0]?.selected?.[0]).not.toBe(input.answers[0]!.selected[0])
  })
})

describe('requested skills', () => {
  it('dedupes by name and trims fields', () => {
    const out = normalizeRequestedSkills([
      { name: ' review ', display_name: ' R ' },
      { name: 'review' },
      { name: '  ' },
    ])
    expect(out).toEqual([{ name: 'review', display_name: 'R', description: undefined, source_kind: undefined, state: undefined }])
  })

  it('cloneRequestedSkills returns fresh objects', () => {
    const src = [{ name: 'a' }]
    const clone = cloneRequestedSkills(src)
    expect(clone).toEqual(src)
    expect(clone[0]).not.toBe(src[0])
  })
})

describe('ids', () => {
  it('serverMessageId prefers serverId', () => {
    expect(serverMessageId(userTurn({ serverId: ' s1 ' } as Partial<ChatUserTurn>))).toBe('s1')
    expect(serverMessageId(userTurn())).toBe('u1')
  })

  it('createInvocationId yields distinct non-empty ids', () => {
    const a = createInvocationId()
    const b = createInvocationId()
    expect(a).toBeTruthy()
    expect(a).not.toBe(b)
  })
})
