import type { HandlersContextLifecycleTurn, HandlersContextTrajectoryEntry } from '@memohai/sdk'
import type { ContextCapturePage, ContextCaptureWindow } from './context-trajectory.types'
import type { TrajectoryRow } from './trajectory-model'

function numericId(id: string | undefined): bigint | null {
  return id && /^\d+$/.test(id) ? BigInt(id) : null
}

function compareIds(a: string | undefined, b: string | undefined): number {
  const left = numericId(a) ?? 0n
  const right = numericId(b) ?? 0n
  return left < right ? -1 : left > right ? 1 : 0
}

function capturePageCoverage(page: ContextCapturePage): { high: bigint, low: bigint } {
  if (page.coverage) return page.coverage
  const first = numericId(page.data.events?.[0]?.id)
  return {
    high: numericId(page.before) ?? (first == null ? 0n : first + 1n),
    low: page.data.has_more ? numericId(page.data.next_cursor) ?? numericId(page.data.events?.at(-1)?.id) ?? 0n : 0n,
  }
}

export function prependContextCapturePage(pages: readonly ContextCapturePage[], page: ContextCapturePage): ContextCapturePage[] {
  if (!page.before && !page.data.has_more) return [page]
  const high = numericId(page.before)
  const low = page.data.has_more ? numericId(page.data.next_cursor) ?? numericId(page.data.events?.at(-1)?.id) : 0n
  return [page, ...pages.flatMap((previous) => {
    const coverage = capturePageCoverage(previous)
    const events = (previous.data.events ?? []).filter((event) => {
      const id = numericId(event.id)
      return id == null || low == null || id < low || (high != null && id >= high)
    })
    const covered = low != null && low <= coverage.low && (high == null || high >= coverage.high)
    return events.length || !covered ? [{ ...previous, coverage, data: { ...previous.data, events } }] : []
  })]
}

export function mergeContextCapturePages(pages: readonly ContextCapturePage[]): ContextCaptureWindow {
  const byId = new Map<string, HandlersContextTrajectoryEntry>()
  const ranges: { high: bigint, low: bigint }[] = []
  let invalidEntries = 0
  for (const page of pages) {
    const events = page.data.events ?? []
    for (const event of events) {
      if (!event.id || numericId(event.id) == null) {
        invalidEntries += 1
        continue
      }
      if (!byId.has(event.id)) byId.set(event.id, event)
    }
    const { high, low } = capturePageCoverage(page)
    if (high >= low) ranges.push({ high, low })
  }
  ranges.sort((a, b) => a.high > b.high ? -1 : a.high < b.high ? 1 : 0)
  let low = ranges[0]?.low ?? 0n
  let gapCursor: string | null = null
  for (const range of ranges.slice(1)) {
    if (range.high < low && gapCursor == null) gapCursor = String(low)
    if (range.low < low) low = range.low
  }
  return {
    events: [...byId.values()].sort((a, b) => compareIds(a.id, b.id)),
    nextCursor: low > 0n ? String(low) : null,
    gapCursor,
    invalidEntries,
  }
}

export function mergeTrajectoryCaptures(
  rows: readonly TrajectoryRow[],
  events: readonly HandlersContextTrajectoryEntry[],
  lifecycles: readonly HandlersContextLifecycleTurn[],
): TrajectoryRow[] {
  const byRun = new Map(lifecycles.map(run => [run.run_id, run]))
  const turnLabels = new Map(rows.map(row => [row.turnId, row.turnLabel]))
  const previous = new Map<string, HandlersContextTrajectoryEntry>()
  const captures: TrajectoryRow[] = []
  for (const event of [...events].sort((a, b) => compareIds(a.id, b.id))) {
    if (!event.id || !event.run_id) continue
    const scope = `${event.run_id}/${event.capture_id ?? ''}`
    const prior = previous.get(scope)
    const turnId = byRun.get(event.run_id)?.turn_id || `run:${event.run_id}`
    const at = Date.parse(event.recorded_at ?? '')
    captures.push({
      key: `capture:${event.id}`,
      kind: event.stage === 'provider_request' || event.stage === 'wire_request' ? 'request' : 'context',
      turnId, turnLabel: turnLabels.get(turnId) ?? (turnId.startsWith('run:') ? event.run_id.slice(0, 8) : '?'), turnStart: false,
      stepIndex: event.step_index ?? null, label: event.stage ?? '', preview: '', output: null,
      startedAtMs: Number.isFinite(at) ? at : null, endedAtMs: null, running: false,
      detail: {
        kind: 'capture', event,
        previousEventId: prior && prior.sequence === (event.sequence ?? 0) - 1 ? prior.id : event.sequence === 1 ? null : undefined,
      },
    })
    previous.set(scope, event)
  }
  const base = rows
  const anchors: number[] = []
  let nextTime = Infinity
  for (let i = base.length - 1; i >= 0; i -= 1) {
    const row = base[i]!
    const outputTrace = (row.kind === 'assistant' || row.kind === 'reasoning') && row.detail.kind === 'block' ? row.detail.trace : null
    const transcriptAt = row.detail.kind === 'user' || row.detail.kind === 'block' ? Date.parse(row.detail.turn.timestamp) : NaN
    nextTime = outputTrace?.first_token_at_ms ?? outputTrace?.ended_at_ms ?? row.startedAtMs ?? (Number.isFinite(transcriptAt) ? transcriptAt : nextTime)
    anchors[i] = nextTime
  }
  const merged: TrajectoryRow[] = []
  let cursor = 0
  base.forEach((row, index) => {
    while (cursor < captures.length && (captures[cursor]!.startedAtMs ?? 0) <= anchors[index]!) merged.push(captures[cursor++]!)
    merged.push(row)
  })
  while (cursor < captures.length) merged.push(captures[cursor++]!)
  return merged.map((row, index) => ({ ...row, turnStart: index === 0 || row.turnId !== merged[index - 1]!.turnId }))
}
