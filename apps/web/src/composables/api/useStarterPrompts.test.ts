import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { resolveStarterPromptSet, useStarterPromptsQuery, type StarterPromptSet } from './useStarterPrompts'

const held: StarterPromptSet = { hash: 'old', items: [{ title: 'Old', prompt: 'Old prompt' }] }
const fresh = { title: 'New', prompt: 'New prompt' }

describe('resolveStarterPromptSet', () => {
  it('keeps the held set when the server reports it unchanged', () => {
    expect(resolveStarterPromptSet(held, { unchanged: true, hash: 'old', items: [] })).toBe(held)
  })

  it('replaces the held set with a newly generated one', () => {
    expect(resolveStarterPromptSet(held, { unchanged: false, hash: 'new', items: [fresh] }))
      .toEqual({ hash: 'new', items: [fresh] })
  })

  it('keeps the held set when no new set could be generated', () => {
    expect(resolveStarterPromptSet(held, { unchanged: false, hash: '', items: [] })).toBe(held)
  })

  it('settles on an empty set when nothing is held and nothing was generated', () => {
    expect(resolveStarterPromptSet(undefined, { unchanged: false, hash: '', items: [] }))
      .toEqual({ hash: '', items: [] })
  })

  it('does not trust unchanged without a held set', () => {
    expect(resolveStarterPromptSet(undefined, { unchanged: true, hash: 'x', items: [] }))
      .toEqual({ hash: '', items: [] })
  })
})


const mocks = vi.hoisted(() => ({ refresh: vi.fn(), set: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdStarterPrompts: vi.fn(),
  postBotsByBotIdStarterPromptsRefresh: mocks.refresh,
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: ref('en') }) }))
vi.mock('@pinia/colada', () => ({
  useQuery: () => ({ error: ref(null) }),
  useQueryCache: () => ({ getQueryData: () => held, setQueryData: mocks.set, cancelQueries: vi.fn() }),
}))

describe('starter prompt refresh failures', () => {
  beforeEach(() => vi.clearAllMocks())

  it('retains the structured error and cached prompts, then clears the error after retry', async () => {
    const problem = { code: 'internal', status: 500, request_id: 'request-123' }
    mocks.refresh.mockRejectedValueOnce(problem)
    const query = useStarterPromptsQuery('bot-1')
    expect(await query.refresh()).toBe(false)
    expect(query.error.value).toEqual(problem)
    expect(query.refreshing.value).toBe(false)
    expect(mocks.set).not.toHaveBeenCalled()
    mocks.refresh.mockResolvedValueOnce({ data: { hash: 'new', items: [fresh] } })
    expect(await query.refresh()).toBe(true)
    expect(query.error.value).toBeNull()
    expect(mocks.set).toHaveBeenCalledWith(['starter-prompts', 'bot-1'], { hash: 'new', items: [fresh] })
  })
})
