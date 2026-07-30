import { describe, expect, it } from 'vitest'
import { canSummarizeRailSegment, clusterRailBlocks, computeBgTaskPill, distinctToolNames, isSessionVisibleInSidebarMode, latestOutputLine, normalizedRuntimeType, normalizedSessionMode, provisionalSessionTitle, reconcileById, segmentHasLiveBg, segmentTurnBlocks, sortByRecency, splitActiveRail, summarizeRailSegment, thinkingPeek, upsertById } from './chat-list.utils'

describe('chat-list.utils', () => {
  it('replaces existing item with same id and preserves order', () => {
    const items = [
      { id: 2, content: 'second' },
      { id: 4, content: 'fourth' },
    ]

    expect(upsertById(items, { id: 2, content: 'updated' })).toEqual([
      { id: 2, content: 'updated' },
      { id: 4, content: 'fourth' },
    ])
  })

  it('inserts new item and sorts by id', () => {
    const items = [
      { id: 4, content: 'fourth' },
      { id: 8, content: 'eighth' },
    ]

    expect(upsertById(items, { id: 6, content: 'sixth' })).toEqual([
      { id: 4, content: 'fourth' },
      { id: 6, content: 'sixth' },
      { id: 8, content: 'eighth' },
    ])
  })

  it('updates an existing item in place, preserving array and item identity', () => {
    const original = { id: 2, content: 'second' }
    const items = [original, { id: 4, content: 'fourth' }]

    const result = upsertById(items, { id: 2, content: 'updated' })

    expect(result).toBe(items)
    expect(result[0]).toBe(original)
    expect(original.content).toBe('updated')
  })

  it('drops fields absent from the incoming snapshot when updating in place', () => {
    const original: { id: number; content: string; stale?: boolean } = {
      id: 2,
      content: 'second',
      stale: true,
    }
    const items = [original]

    upsertById(items, { id: 2, content: 'updated' })

    expect(original).toEqual({ id: 2, content: 'updated' })
  })

  it('reconcileById reuses matched items in place and follows incoming order', () => {
    const a = { id: 1, v: 'a' }
    const b = { id: 2, v: 'b' }
    const target = [a, b]

    const result = reconcileById(target, [
      { id: 2, v: 'b2' },
      { id: 1, v: 'a2' },
    ])

    expect(result).toBe(target)
    expect(result[0]).toBe(b)
    expect(result[1]).toBe(a)
    expect(a.v).toBe('a2')
    expect(b.v).toBe('b2')
    expect(result.map(x => x.id)).toEqual([2, 1])
  })

  it('reconcileById drops items absent from incoming and inserts new ones', () => {
    const a = { id: 1, v: 'a' }
    const target = [a, { id: 2, v: 'b' }]

    const result = reconcileById(target, [
      { id: 1, v: 'a' },
      { id: 3, v: 'c' },
    ])

    expect(result[0]).toBe(a)
    expect(result.map(x => x.id)).toEqual([1, 3])
  })

  it('reconcileById matches existing items via a custom key', () => {
    const optimistic = { id: 'client-1', serverId: 'server-1', v: 'old' }
    const target: Array<{ id: string; serverId?: string; v: string }> = [optimistic]

    reconcileById(target, [{ id: 'server-1', v: 'new' }], {
      keyOfExisting: item => item.serverId ?? item.id,
    })

    expect(target[0]).toBe(optimistic)
    expect(optimistic.v).toBe('new')
  })

  it('reconcileById applies a custom merge to matched items', () => {
    const a = { id: 1, items: ['x'] }
    const target = [a]

    reconcileById(target, [{ id: 1, items: ['x', 'y'] }], {
      merge: (cur, inc) => {
        cur.items = inc.items
      },
    })

    expect(target[0]).toBe(a)
    expect(a.items).toEqual(['x', 'y'])
  })

  it('sortByRecency orders by updated_at desc, falls back to created_at, stable on ties', () => {
    const a = { id: 'a', updated_at: '2026-01-01T00:00:00Z' }
    const b = { id: 'b', updated_at: '2026-01-03T00:00:00Z' }
    const c = { id: 'c', created_at: '2026-01-02T00:00:00Z' }
    const d = { id: 'd' }
    const e = { id: 'e', updated_at: '2026-01-03T00:00:00Z' }

    expect(sortByRecency<{
      id: string
      updated_at?: string
      created_at?: string
    }>([a, b, c, d, e]).map(x => x.id)).toEqual(['b', 'e', 'c', 'a', 'd'])
  })

  it('sortByRecency does not mutate its input', () => {
    const input = [
      { id: 'a', updated_at: '2026-01-01T00:00:00Z' },
      { id: 'b', updated_at: '2026-01-03T00:00:00Z' },
    ]

    sortByRecency(input)

    expect(input.map(x => x.id)).toEqual(['a', 'b'])
  })

  it('keeps ACP chat sessions in Recent while also allowing the Agent filter', () => {
    const acpChat = {
      type: 'acp_agent',
      session_mode: 'chat',
      runtime_type: 'acp_agent',
    }

    expect(isSessionVisibleInSidebarMode(acpChat, 'recent')).toBe(true)
    expect(isSessionVisibleInSidebarMode(acpChat, 'agent')).toBe(true)
    expect(isSessionVisibleInSidebarMode(acpChat, 'schedule')).toBe(false)
  })

  it('normalizes legacy ACP session descriptors for sidebar filtering', () => {
    const legacyACPChat = { type: 'acp_agent' }
    const schedule = { type: 'schedule' }

    expect(normalizedSessionMode(legacyACPChat)).toBe('chat')
    expect(normalizedRuntimeType(legacyACPChat)).toBe('acp_agent')
    expect(isSessionVisibleInSidebarMode(legacyACPChat, 'recent')).toBe(true)
    expect(isSessionVisibleInSidebarMode(legacyACPChat, 'agent')).toBe(true)
    expect(isSessionVisibleInSidebarMode(schedule, 'schedule')).toBe(true)
    expect(isSessionVisibleInSidebarMode(schedule, 'recent')).toBe(false)
  })

  it('latestOutputLine returns the last non-empty line, trimmed', () => {
    expect(latestOutputLine('alpha\nbeta\ngamma')).toBe('gamma')
    expect(latestOutputLine('alpha\nbeta\n')).toBe('beta')
    expect(latestOutputLine('alpha\n\n')).toBe('alpha')
    expect(latestOutputLine('  hi  ')).toBe('hi')
  })

  it('latestOutputLine collapses carriage-return progress to the current segment', () => {
    expect(latestOutputLine('downloading 10%\rdownloading 80%')).toBe('downloading 80%')
    expect(latestOutputLine('build\nstep 1\r')).toBe('step 1')
  })

  it('latestOutputLine returns empty for empty, whitespace, or missing input', () => {
    expect(latestOutputLine('')).toBe('')
    expect(latestOutputLine(undefined)).toBe('')
    expect(latestOutputLine('\r\n')).toBe('')
    expect(latestOutputLine('   \n  ')).toBe('')
  })

})

describe('segmentTurnBlocks', () => {
  const b = (id: number, type: string) => ({ id, type })

  it('returns no segments for an empty turn', () => {
    expect(segmentTurnBlocks([])).toEqual([])
  })

  it('wraps a lone process block in a rail segment keyed by its id', () => {
    const tool = b(1, 'tool')
    expect(segmentTurnBlocks([tool])).toEqual([
      { kind: 'rail', key: 'rail:1', blocks: [tool] },
    ])
  })

  it('emits text / error / attachments as standalone flow segments', () => {
    const text = b(1, 'text')
    const error = b(2, 'error')
    const attachments = b(3, 'attachments')
    expect(segmentTurnBlocks([text, error, attachments])).toEqual([
      { kind: 'flow', key: 'flow:1', block: text },
      { kind: 'flow', key: 'flow:2', block: error },
      { kind: 'flow', key: 'flow:3', block: attachments },
    ])
  })

  it('coalesces a maximal run of consecutive process blocks into one rail', () => {
    const reasoning = b(1, 'reasoning')
    const tool1 = b(2, 'tool')
    const tool2 = b(3, 'tool')
    expect(segmentTurnBlocks([reasoning, tool1, tool2])).toEqual([
      { kind: 'rail', key: 'rail:1', blocks: [reasoning, tool1, tool2] },
    ])
  })

  it('splits a process run when a flow block interrupts it', () => {
    const tool1 = b(1, 'tool')
    const text = b(2, 'text')
    const tool2 = b(3, 'tool')
    expect(segmentTurnBlocks([tool1, text, tool2])).toEqual([
      { kind: 'rail', key: 'rail:1', blocks: [tool1] },
      { kind: 'flow', key: 'flow:2', block: text },
      { kind: 'rail', key: 'rail:3', blocks: [tool2] },
    ])
  })

  it('keys each segment by its first block so tail growth never reparents earlier segments', () => {
    const tool1 = b(1, 'tool')
    const text = b(2, 'text')
    const tool2 = b(3, 'tool')
    const reasoning = b(4, 'reasoning')

    const before = segmentTurnBlocks([tool1, text, tool2])
    const after = segmentTurnBlocks([tool1, text, tool2, reasoning])

    expect(after[0]!.key).toBe(before[0]!.key)
    expect(after[1]!.key).toBe(before[1]!.key)
    expect(after[2]!.key).toBe(before[2]!.key)
    expect(after[2]).toEqual({ kind: 'rail', key: 'rail:3', blocks: [tool2, reasoning] })
  })

  it('preserves block object identity inside segments', () => {
    const tool = b(1, 'tool')
    const text = b(2, 'text')
    const result = segmentTurnBlocks([tool, text])
    expect((result[0] as { blocks: unknown[] }).blocks[0]).toBe(tool)
    expect((result[1] as { block: unknown }).block).toBe(text)
  })
})

describe('clusterRailBlocks', () => {
  const tool = (id: number, done: boolean, toolName = 'exec') => ({ id, type: 'tool', toolName, done })
  const reasoning = (id: number) => ({ id, type: 'reasoning' })

  it('returns no items for an empty rail', () => {
    expect(clusterRailBlocks([])).toEqual([])
  })

  it('keeps a single done tool solo (a run of one never folds)', () => {
    const t = tool(1, true)
    expect(clusterRailBlocks([t])).toEqual([
      { kind: 'block', key: 'block:1', block: t },
    ])
  })

  it('folds a run of two or more consecutive done tools into a cluster', () => {
    const t1 = tool(1, true)
    const t2 = tool(2, true)
    const t3 = tool(3, true)
    expect(clusterRailBlocks([t1, t2, t3])).toEqual([
      { kind: 'cluster', key: 'cluster:1', tools: [t1, t2, t3] },
    ])
  })

  it('renders an in-progress tool solo and lets it break a done run', () => {
    const t1 = tool(1, true)
    const t2 = tool(2, true)
    const running = tool(3, false)
    expect(clusterRailBlocks([t1, t2, running])).toEqual([
      { kind: 'cluster', key: 'cluster:1', tools: [t1, t2] },
      { kind: 'block', key: 'block:3', block: running },
    ])
  })

  it('lets a reasoning block break a run and render solo', () => {
    const t1 = tool(1, true)
    const r2 = reasoning(2)
    const t3 = tool(3, true)
    const t4 = tool(4, true)
    expect(clusterRailBlocks([t1, r2, t3, t4])).toEqual([
      { kind: 'block', key: 'block:1', block: t1 },
      { kind: 'block', key: 'block:2', block: r2 },
      { kind: 'cluster', key: 'cluster:3', tools: [t3, t4] },
    ])
  })

  it('folds nothing anywhere while the turn streams (keepOpen) so no tool ever reparents mid-stream', () => {
    const t1 = tool(1, true)
    const t2 = tool(2, true)
    const r3 = reasoning(3)
    const t4 = tool(4, true)
    const t5 = tool(5, true)
    expect(clusterRailBlocks([t1, t2, r3, t4, t5], true)).toEqual([
      { kind: 'block', key: 'block:1', block: t1 },
      { kind: 'block', key: 'block:2', block: t2 },
      { kind: 'block', key: 'block:3', block: r3 },
      { kind: 'block', key: 'block:4', block: t4 },
      { kind: 'block', key: 'block:5', block: t5 },
    ])
  })

  it('preserves tool object identity inside clusters', () => {
    const t1 = tool(1, true)
    const t2 = tool(2, true)
    const result = clusterRailBlocks([t1, t2])
    expect((result[0] as { tools: unknown[] }).tools[0]).toBe(t1)
  })

  it('never folds a done tool awaiting approval, so its controls stay visible', () => {
    const t1 = tool(1, true)
    const awaitingApproval = { id: 2, type: 'tool', toolName: 'exec', done: true, approval: { status: 'pending' } }
    expect(clusterRailBlocks([t1, awaitingApproval])).toEqual([
      { kind: 'block', key: 'block:1', block: t1 },
      { kind: 'block', key: 'block:2', block: awaitingApproval },
    ])
  })

  it('never folds a done tool awaiting user input', () => {
    const t1 = tool(1, true)
    const awaitingInput = { id: 2, type: 'tool', toolName: 'ask_user', done: true, userInput: { status: 'pending' } }
    expect(clusterRailBlocks([t1, awaitingInput])).toEqual([
      { kind: 'block', key: 'block:1', block: t1 },
      { kind: 'block', key: 'block:2', block: awaitingInput },
    ])
  })

  it('still folds done tools whose approval has resolved', () => {
    const resolved = { id: 1, type: 'tool', toolName: 'exec', done: true, approval: { status: 'approved' } }
    const t2 = tool(2, true)
    expect(clusterRailBlocks([resolved, t2])).toEqual([
      { kind: 'cluster', key: 'cluster:1', tools: [resolved, t2] },
    ])
  })

  it('never folds a tool whose background task is still running, so its live line stays visible', () => {
    const t1 = tool(1, true)
    const liveBg = { id: 2, type: 'tool', toolName: 'exec', done: true, backgroundTask: { status: 'running' } }
    expect(clusterRailBlocks([t1, liveBg])).toEqual([
      { kind: 'block', key: 'block:1', block: t1 },
      { kind: 'block', key: 'block:2', block: liveBg },
    ])
  })

  it('folds a tool whose background task has completed', () => {
    const finishedBg = { id: 1, type: 'tool', toolName: 'exec', done: true, backgroundTask: { status: 'completed' } }
    const t2 = tool(2, true)
    expect(clusterRailBlocks([finishedBg, t2])).toEqual([
      { kind: 'cluster', key: 'cluster:1', tools: [finishedBg, t2] },
    ])
  })
})

describe('summarizeRailSegment', () => {
  it('counts thinking and tool blocks and lists distinct tool names in order', () => {
    const blocks = [
      { id: 1, type: 'reasoning' },
      { id: 2, type: 'tool', toolName: 'exec' },
      { id: 3, type: 'tool', toolName: 'edit' },
      { id: 4, type: 'tool', toolName: 'exec' },
      { id: 5, type: 'reasoning' },
    ]
    expect(summarizeRailSegment(blocks)).toEqual({
      thinkingCount: 2,
      toolCount: 3,
      toolNames: ['exec', 'edit'],
    })
  })

  it('returns zeroed counts for an empty segment', () => {
    expect(summarizeRailSegment([])).toEqual({ thinkingCount: 0, toolCount: 0, toolNames: [] })
  })
})

describe('canSummarizeRailSegment', () => {
  const doneTool = (id: number) => ({ id, type: 'tool', toolName: 'exec', done: true })

  it('summarizes a settled segment of two or more blocks', () => {
    expect(canSummarizeRailSegment([{ id: 1, type: 'reasoning' }, doneTool(2)])).toBe(true)
  })

  it('does not summarize a segment with fewer than two blocks', () => {
    expect(canSummarizeRailSegment([doneTool(1)])).toBe(false)
  })

  it('does not summarize while a tool is still running', () => {
    expect(canSummarizeRailSegment([doneTool(1), { id: 2, type: 'tool', toolName: 'exec', done: false }])).toBe(false)
  })

  it('does not summarize while a tool awaits approval or input', () => {
    expect(canSummarizeRailSegment([doneTool(1), { id: 2, type: 'tool', done: true, approval: { status: 'pending' } }])).toBe(false)
    expect(canSummarizeRailSegment([doneTool(1), { id: 2, type: 'tool', done: true, userInput: { status: 'pending' } }])).toBe(false)
  })

  it('does not summarize while a background task is still running', () => {
    expect(canSummarizeRailSegment([doneTool(1), { id: 2, type: 'tool', done: true, backgroundTask: { status: 'running' } }])).toBe(false)
  })

  it('summarizes a segment whose background task has completed', () => {
    expect(canSummarizeRailSegment([doneTool(1), { id: 2, type: 'tool', done: true, backgroundTask: { status: 'completed' } }])).toBe(true)
  })
})

describe('splitActiveRail', () => {
  const b = (id: number, type: string) => ({ id, type })

  it('returns no current group for an empty segment', () => {
    expect(splitActiveRail([])).toEqual({ prior: [], current: null })
  })

  it('makes a lone tool the current tools group with no prior', () => {
    const t = b(1, 'tool')
    expect(splitActiveRail([t])).toEqual({ prior: [], current: { kind: 'tools', blocks: [t] } })
  })

  it('groups a trailing tool run as current and earlier blocks as prior', () => {
    const r = b(1, 'reasoning')
    const t1 = b(2, 'tool')
    const t2 = b(3, 'tool')
    expect(splitActiveRail([r, t1, t2])).toEqual({ prior: [r], current: { kind: 'tools', blocks: [t1, t2] } })
  })

  it('makes a trailing thinking block the current think group', () => {
    const t = b(1, 'tool')
    const r = b(2, 'reasoning')
    expect(splitActiveRail([t, r])).toEqual({ prior: [t], current: { kind: 'think', blocks: [r] } })
  })

  it('flattens every earlier group into prior', () => {
    const r1 = b(1, 'reasoning')
    const t1 = b(2, 'tool')
    const r2 = b(3, 'reasoning')
    const t2 = b(4, 'tool')
    const t3 = b(5, 'tool')
    expect(splitActiveRail([r1, t1, r2, t2, t3])).toEqual({
      prior: [r1, t1, r2],
      current: { kind: 'tools', blocks: [t2, t3] },
    })
  })
})

describe('thinkingPeek', () => {
  it('returns empty for empty content', () => {
    expect(thinkingPeek('')).toBe('')
    expect(thinkingPeek(undefined)).toBe('')
  })

  it('returns the last complete sentence, ignoring an in-progress one', () => {
    expect(thinkingPeek('First idea here. Now I am starting to')).toBe('First idea here.')
  })

  it('returns the latest of several complete sentences', () => {
    expect(thinkingPeek('One. Two. Three.')).toBe('Three.')
  })

  it('strips heading / emphasis / inline-code markers, keeping the prose', () => {
    expect(thinkingPeek('## Plan\nI will check **the** `config` value.')).toBe('I will check the config value.')
  })

  it('skips fenced code blocks and shows the surrounding prose', () => {
    expect(thinkingPeek('Let me run this:\n```\nnpm test\n```\nThe suite passed.')).toBe('The suite passed.')
  })

  it('strips list markers, showing clean item text', () => {
    expect(thinkingPeek('- **First** item\n- second item')).toBe('second item')
  })

  it('falls back to the in-progress fragment when no sentence has completed', () => {
    expect(thinkingPeek('Considering the options for')).toBe('Considering the options for')
  })
})

describe('segmentHasLiveBg', () => {
  it('detects a running or stalled background task in a tool block', () => {
    expect(segmentHasLiveBg([{ id: 1, type: 'tool', backgroundTask: { status: 'running' } }])).toBe(true)
    expect(segmentHasLiveBg([{ id: 1, type: 'tool', backgroundTask: { status: 'stalled' } }])).toBe(true)
  })

  it('ignores completed or absent background tasks', () => {
    expect(segmentHasLiveBg([{ id: 1, type: 'tool', backgroundTask: { status: 'completed' } }])).toBe(false)
    expect(segmentHasLiveBg([{ id: 1, type: 'tool' }, { id: 2, type: 'reasoning' }])).toBe(false)
    expect(segmentHasLiveBg([])).toBe(false)
  })
})

describe('distinctToolNames', () => {
  it('returns tool names in first-seen order without duplicates', () => {
    const tools = [
      { id: 1, toolName: 'exec' },
      { id: 2, toolName: 'exec' },
      { id: 3, toolName: 'edit' },
      { id: 4, toolName: 'exec' },
    ]
    expect(distinctToolNames(tools)).toEqual(['exec', 'edit'])
  })

  it('returns an empty list for no tools', () => {
    expect(distinctToolNames([])).toEqual([])
  })
})

describe('computeBgTaskPill', () => {
  const beacon = (taskId: string, phase: 'active' | 'done', visible: boolean, latestLine = '') =>
    ({ taskId, phase, visible, latestLine })

  it('shows no pill when there are no beacons', () => {
    expect(computeBgTaskPill([])).toBeNull()
  })

  it('shows no pill when the only running task is on screen', () => {
    expect(computeBgTaskPill([beacon('t1', 'active', true, 'step 1')])).toBeNull()
  })

  it('shows a running pill for an off-screen running task', () => {
    expect(computeBgTaskPill([beacon('t1', 'active', false, 'step 7')])).toEqual({
      tone: 'running',
      count: 1,
      latestLine: 'step 7',
    })
  })

  it('counts only off-screen running tasks and uses the latest one for the line', () => {
    expect(computeBgTaskPill([
      beacon('t1', 'active', false, 'first'),
      beacon('t2', 'active', true, 'on screen'),
      beacon('t3', 'active', false, 'latest'),
    ])).toEqual({ tone: 'running', count: 2, latestLine: 'latest' })
  })

  it('shows a done pill when an off-screen task has completed and none are running', () => {
    expect(computeBgTaskPill([beacon('t1', 'done', false)])).toEqual({
      tone: 'done',
      count: 1,
      latestLine: '',
    })
  })

  it('prefers the running tone over done when both are off-screen', () => {
    expect(computeBgTaskPill([
      beacon('t1', 'done', false),
      beacon('t2', 'active', false, 'working'),
    ])).toEqual({ tone: 'running', count: 1, latestLine: 'working' })
  })
})

describe('provisionalSessionTitle', () => {
  it('returns the trimmed first line as-is when short', () => {
    expect(provisionalSessionTitle('How do I parse JSON in Go?')).toBe('How do I parse JSON in Go?')
  })

  it('uses only the first line, dropping the rest', () => {
    expect(provisionalSessionTitle('Summarize this file\n\nHere is the content:\nlots\nof\nlines')).toBe('Summarize this file')
  })

  it('truncates to the cap and appends an ellipsis', () => {
    expect(provisionalSessionTitle('A'.repeat(120))).toBe('A'.repeat(50) + '…')
  })

  it('counts runes, not utf-16 units, when truncating CJK text', () => {
    expect(provisionalSessionTitle('あ'.repeat(80))).toBe('あ'.repeat(50) + '…')
  })

  it('strips markdown so the label stays a clean one-line peek', () => {
    expect(provisionalSessionTitle('## Title\n`code` and [a link](https://x)')).toBe('Title')
  })

  it('strips emphasis markers so it matches the backend fallback', () => {
    expect(provisionalSessionTitle('**bold** and *italic* here')).toBe('bold and italic here')
  })

  it('returns empty when the first line has no usable text', () => {
    expect(provisionalSessionTitle('```js\nconst x = 1\n```')).toBe('')
  })
})
