import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { applyFsChangedPaths, createFsChangeBeacon } from './fs-beacon'
import type { UIMessage } from '@/composables/api/useChat'

function toolMessage(overrides: Partial<Record<string, unknown>> = {}): UIMessage {
  return {
    id: 1,
    type: 'tool',
    name: 'write',
    running: false,
    tool_call_id: 'call-1',
    input: { path: '/ws/file.txt', content: 'hello' },
    ...overrides,
  } as unknown as UIMessage
}

describe('fs-change beacon', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  function makeBeacon(botId = 'bot-1', sessionId = 'sess-1') {
    const currentBotId = ref<string | null>(botId)
    const sessionIdRef = ref<string | null>(sessionId)
    return { beacon: createFsChangeBeacon({ currentBotId, sessionId: sessionIdRef }), currentBotId, sessionIdRef }
  }

  it('collapses a burst of marks within the debounce window into one bump', () => {
    const { beacon } = makeBeacon()
    beacon.markFsChanged('/a')
    beacon.markFsChanged('/b')
    expect(beacon.fsChangedAt.value).toBe(0)
    vi.advanceTimersByTime(150)
    expect(beacon.fsChangedAt.value).toBeGreaterThan(0)
    expect([...beacon.lastFsChange.value!.paths!]).toEqual(['/a', '/b'])
  })

  it('a wildcard mark poisons the whole batch to paths=null', () => {
    const { beacon } = makeBeacon()
    beacon.markFsChanged('/a')
    beacon.markFsChanged(null)
    vi.advanceTimersByTime(150)
    expect(beacon.lastFsChange.value!.paths).toBeNull()
    expect(beacon.affectsPath('/anything')).toBe(true)
  })

  it('drops the in-flight batch when the bot changes before the timer fires', () => {
    const { beacon, currentBotId } = makeBeacon('bot-1')
    beacon.markFsChanged('/a')
    currentBotId.value = 'bot-2'
    vi.advanceTimersByTime(150)
    expect(beacon.fsChangedAt.value).toBe(0)
    expect(beacon.lastFsChange.value).toBeNull()
  })

  it('affectsPath matches only batched paths for a path-targeted batch', () => {
    const { beacon } = makeBeacon()
    beacon.markFsChanged('/a')
    vi.advanceTimersByTime(150)
    expect(beacon.affectsPath('/a')).toBe(true)
    expect(beacon.affectsPath('/b')).toBe(false)
  })

  it('bumpFsChangedAtIfFsMutation dedupes by tool_call_id across WS + refresh replays', () => {
    const { beacon } = makeBeacon()
    beacon.bumpFsChangedAtIfFsMutation(toolMessage())
    vi.advanceTimersByTime(150)
    const first = beacon.fsChangedAt.value
    beacon.bumpFsChangedAtIfFsMutation(toolMessage())
    vi.advanceTimersByTime(150)
    expect(beacon.fsChangedAt.value).toBe(first)
  })

  it('ignores running tools and non-fs tools', () => {
    const { beacon } = makeBeacon()
    beacon.bumpFsChangedAtIfFsMutation(toolMessage({ running: true }))
    beacon.bumpFsChangedAtIfFsMutation(toolMessage({ name: 'web_search', tool_call_id: 'call-2' }))
    vi.advanceTimersByTime(150)
    expect(beacon.fsChangedAt.value).toBe(0)
  })

  it('records a rich per-path event for write, stamped with the active session', () => {
    const { beacon } = makeBeacon('bot-1', ' sess-9 ')
    beacon.bumpFsChangedAtIfFsMutation(toolMessage({ tool_call_id: 'call-3' }))
    vi.advanceTimersByTime(150)
    const event = beacon.fsEventForPath('/ws/file.txt')
    expect(event).toMatchObject({
      path: '/ws/file.txt',
      kind: 'write',
      toolCallId: 'call-3',
      sessionId: 'sess-9',
      writeContent: 'hello',
    })
  })

  it('relative paths and exec fall back to wildcard with no per-path event', () => {
    const { beacon } = makeBeacon()
    beacon.bumpFsChangedAtIfFsMutation(toolMessage({ tool_call_id: 'c1', input: { path: 'rel.txt', content: 'x' } }))
    beacon.bumpFsChangedAtIfFsMutation(toolMessage({ tool_call_id: 'c2', name: 'exec', input: { command: 'rm -rf /tmp/x' } }))
    vi.advanceTimersByTime(150)
    expect(beacon.lastFsChange.value!.paths).toBeNull()
    expect(beacon.fsEventForPath('rel.txt')).toBeNull()
  })

  it('resetFsBeacon zeroes the watermark; clearFsForBotSwitch keeps it', () => {
    const { beacon } = makeBeacon()
    beacon.markFsChanged('/a')
    vi.advanceTimersByTime(150)
    const stamped = beacon.fsChangedAt.value
    expect(stamped).toBeGreaterThan(0)

    beacon.clearFsForBotSwitch()
    expect(beacon.fsChangedAt.value).toBe(stamped)
    expect(beacon.lastFsChange.value).toBeNull()

    beacon.resetFsBeacon()
    expect(beacon.fsChangedAt.value).toBe(0)
  })

  it('cancelPendingFsBump drops queued paths without firing', () => {
    const { beacon } = makeBeacon()
    beacon.markFsChanged('/a')
    beacon.cancelPendingFsBump()
    vi.advanceTimersByTime(300)
    expect(beacon.fsChangedAt.value).toBe(0)
    expect(beacon.lastFsChange.value).toBeNull()
  })
})

describe('applyFsChangedPaths', () => {
  function record() {
    const marks: (string | undefined)[] = []
    const mark = (path?: string | null) => { marks.push(path ?? undefined) }
    return { marks, mark }
  }

  it('marks each path from a server fs_changed event', () => {
    const { marks, mark } = record()
    applyFsChangedPaths(mark, ['/data/a.txt', '/data/b.txt'])
    expect(marks).toEqual(['/data/a.txt', '/data/b.txt'])
  })

  it('marks a wildcard for a null payload', () => {
    const { marks, mark } = record()
    applyFsChangedPaths(mark, null)
    expect(marks).toEqual([undefined])
  })

  it('marks a wildcard for an undefined payload', () => {
    const { marks, mark } = record()
    applyFsChangedPaths(mark, undefined)
    expect(marks).toEqual([undefined])
  })

  it('ignores an empty payload', () => {
    const { marks, mark } = record()
    applyFsChangedPaths(mark, [])
    expect(marks).toEqual([])
  })
})
