import type { HandlersContextLifecycleTurn } from '@memohai/sdk'
import type { UIStepTrace } from '@/composables/api/useChat.types'
import type { ChatAssistantTurn, ChatMessage, ChatUserTurn, ContentBlock, ToolCallBlock } from '@/store/chat/types'

export const PREVIEW_SOURCE_CHARACTERS = 2048
export const PREVIEW_OUTPUT_CHARACTERS = 512

export type TrajectoryRowKind = 'system' | 'user' | 'context' | 'assistant' | 'reasoning' | 'tool' | 'error' | 'notice'

export type TrajectoryDetail =
  | { kind: 'user', turn: ChatUserTurn }
  | { kind: 'system', turn: ChatAssistantTurn, lifecycle: HandlersContextLifecycleTurn }
  | { kind: 'block', turn: ChatAssistantTurn, block: ContentBlock, trace: UIStepTrace | null }

export interface TrajectoryRow {
  key: string
  kind: TrajectoryRowKind
  turnId: string
  // Server-issued turn position when the settled row carries one, else the
  // turn's ordinal inside the loaded window.
  turnLabel: string
  turnStart: boolean
  stepIndex: number | null
  label: string
  preview: string
  output: string | null
  startedAtMs: number | null
  endedAtMs: number | null
  running: boolean
  detail: TrajectoryDetail
}

export type TimelineLane = 'input' | 'model' | 'tools'

export interface TimelineSpan {
  lane: TimelineLane
  key: string
  start: number
  end: number
  ttftEnd: number | null
  label: string
  tokens: number
  cachedTokens: number
  stepIndex: number | null
}

export interface TurnTimeline {
  spans: TimelineSpan[]
  start: number
  end: number
  steps: number
}

export interface TrajectoryStats {
  turns: number
  steps: number
  toolCalls: number
  llmMs: number
  toolMs: number
  ttftAvgMs: number | null
  decodeMs: number
  decodeTokens: number
  inputTokens: number
  cachedInputTokens: number
  outputTokens: number
}

export function lifecycleByTurnId(turns: HandlersContextLifecycleTurn[]): Map<string, HandlersContextLifecycleTurn> {
  const byTurn = new Map<string, HandlersContextLifecycleTurn>()
  for (const turn of turns) {
    const turnId = turn.turn_id?.trim()
    if (turnId && !byTurn.has(turnId)) byTurn.set(turnId, turn)
  }
  return byTurn
}

export function previewText(value: unknown, limit: number): string {
  if (value == null) return ''
  let text: string
  if (typeof value === 'string') {
    text = value
  } else {
    try {
      text = JSON.stringify(value) ?? ''
    } catch {
      text = String(value)
    }
  }
  const flat = text.replace(/\s+/g, ' ').trim()
  return flat.length > limit ? `${flat.slice(0, Math.max(limit - 1, 0))}…` : flat
}

// Blocks belong to the last request whose anchor is at or before them.
export function stepIndexForBlock(traces: UIStepTrace[] | undefined, blockId: number): number | null {
  if (!traces?.length) return null
  let match: UIStepTrace | null = null
  for (const trace of traces) {
    if (trace.first_message_id <= blockId && (!match || trace.first_message_id >= match.first_message_id)) match = trace
  }
  return match?.step_index ?? null
}

function traceForBlock(traces: UIStepTrace[] | undefined, blockId: number): UIStepTrace | null {
  const stepIndex = stepIndexForBlock(traces, blockId)
  return stepIndex == null ? null : traces!.find(trace => trace.step_index === stepIndex) ?? null
}

function toolTiming(block: ToolCallBlock): { start: number, end: number } | null {
  const timing = block.execution_timing
  if (!timing || timing.started_at_ms <= 0 || timing.ended_at_ms < timing.started_at_ms) return null
  return { start: timing.started_at_ms, end: timing.ended_at_ms }
}

function blockRow(turn: ChatAssistantTurn, block: ContentBlock, turnLabel: string): TrajectoryRow | null {
  const trace = traceForBlock(turn.stepTraces, block.id)
  const base = {
    key: `${turn.id}:block:${block.id}`,
    turnId: turn.turnId ?? '',
    turnLabel,
    turnStart: false,
    stepIndex: trace?.step_index ?? null,
    startedAtMs: trace?.started_at_ms ?? null,
    endedAtMs: trace?.ended_at_ms ?? null,
    running: false,
    output: null,
    detail: { kind: 'block' as const, turn, block, trace },
  }
  switch (block.type) {
    case 'text':
      return { ...base, kind: 'assistant', label: '', preview: previewText(block.content, PREVIEW_SOURCE_CHARACTERS) }
    case 'reasoning':
      return { ...base, kind: 'reasoning', label: '', preview: previewText(block.content, PREVIEW_SOURCE_CHARACTERS) }
    case 'tool': {
      const timing = toolTiming(block)
      return {
        ...base,
        kind: 'tool',
        label: block.toolName || block.name,
        preview: previewText(block.input, PREVIEW_SOURCE_CHARACTERS),
        output: block.running ? null : previewText(block.result ?? block.output, PREVIEW_OUTPUT_CHARACTERS),
        startedAtMs: timing?.start ?? null,
        endedAtMs: timing?.end ?? null,
        running: block.running,
      }
    }
    case 'error':
      return { ...base, kind: 'error', label: block.code ?? '', preview: previewText(block.content, PREVIEW_OUTPUT_CHARACTERS) }
    case 'notice':
      return { ...base, kind: 'notice', label: block.name ?? '', preview: previewText(block.content, PREVIEW_OUTPUT_CHARACTERS) }
    default:
      return null
  }
}

export function buildTrajectoryRows(
  messages: ChatMessage[],
  lifecycleByTurn: ReadonlyMap<string, HandlersContextLifecycleTurn>,
): TrajectoryRow[] {
  const rows: TrajectoryRow[] = []
  const ordinals = new Map<string, number>()
  let lastTurnId: string | null = null
  const labelFor = (turn: ChatMessage): string => {
    const turnId = turn.turnId ?? ''
    if (turn.turnPosition != null) return String(turn.turnPosition)
    const key = turnId || turn.id
    if (!ordinals.has(key)) ordinals.set(key, ordinals.size + 1)
    return String(ordinals.get(key))
  }
  const markTurnStart = (turn: ChatMessage, row: TrajectoryRow) => {
    const turnKey = turn.turnId || turn.id
    row.turnStart = turnKey !== lastTurnId
    lastTurnId = turnKey
  }
  for (const turn of messages) {
    if (turn.role === 'system') continue
    const turnLabel = labelFor(turn)
    if (turn.role === 'user') {
      const injection = turn.contextInjection?.kind?.trim() ?? ''
      const row: TrajectoryRow = {
        key: `${turn.id}:user`,
        kind: injection ? 'context' : 'user',
        turnId: turn.turnId ?? '',
        turnLabel,
        turnStart: false,
        stepIndex: null,
        label: injection,
        preview: previewText(turn.text, PREVIEW_SOURCE_CHARACTERS),
        output: null,
        startedAtMs: null,
        endedAtMs: null,
        running: false,
        detail: { kind: 'user', turn },
      }
      markTurnStart(turn, row)
      rows.push(row)
      continue
    }
    const lifecycle = turn.turnId ? lifecycleByTurn.get(turn.turnId) : undefined
    const turnRows: TrajectoryRow[] = []
    if (lifecycle) {
      turnRows.push({
        key: `${turn.id}:system`,
        kind: 'system',
        turnId: turn.turnId ?? '',
        turnLabel,
        turnStart: false,
        stepIndex: null,
        label: '',
        preview: '',
        output: null,
        startedAtMs: null,
        endedAtMs: null,
        running: false,
        detail: { kind: 'system', turn, lifecycle },
      })
    }
    for (const block of turn.messages) {
      const row = blockRow(turn, block, turnLabel)
      if (row) turnRows.push(row)
    }
    if (turnRows.length === 0) continue
    markTurnStart(turn, turnRows[0]!)
    rows.push(...turnRows)
  }
  return rows
}

export function buildTurnTimeline(turn: ChatAssistantTurn): TurnTimeline | null {
  const spans: TimelineSpan[] = []
  for (const trace of turn.stepTraces ?? []) {
    if (trace.started_at_ms <= 0 || trace.ended_at_ms < trace.started_at_ms) continue
    const ttft = trace.first_token_at_ms && trace.first_token_at_ms >= trace.started_at_ms && trace.first_token_at_ms <= trace.ended_at_ms
      ? trace.first_token_at_ms
      : null
    spans.push({
      lane: 'model',
      key: `model:${trace.step_index}`,
      start: trace.started_at_ms,
      end: trace.ended_at_ms,
      ttftEnd: ttft,
      label: trace.finish_reason ?? '',
      tokens: trace.usage?.output_tokens ?? 0,
      cachedTokens: 0,
      stepIndex: trace.step_index,
    })
    spans.push({
      lane: 'input',
      key: `input:${trace.step_index}`,
      start: trace.started_at_ms,
      end: trace.ended_at_ms,
      ttftEnd: null,
      label: '',
      tokens: trace.usage?.input_tokens ?? 0,
      cachedTokens: trace.usage?.cached_input_tokens ?? 0,
      stepIndex: trace.step_index,
    })
  }
  for (const block of turn.messages) {
    if (block.type !== 'tool') continue
    const timing = toolTiming(block)
    if (!timing) continue
    spans.push({
      lane: 'tools',
      key: `tool:${block.id}`,
      start: timing.start,
      end: timing.end,
      ttftEnd: null,
      label: block.toolName || block.name,
      tokens: 0,
      cachedTokens: 0,
      stepIndex: stepIndexForBlock(turn.stepTraces, block.id),
    })
  }
  if (spans.length === 0) return null
  return {
    spans,
    start: Math.min(...spans.map(span => span.start)),
    end: Math.max(...spans.map(span => span.end)),
    steps: turn.stepTraces?.length ?? 0,
  }
}

function emptyStats(): TrajectoryStats {
  return { turns: 0, steps: 0, toolCalls: 0, llmMs: 0, toolMs: 0, ttftAvgMs: null, decodeMs: 0, decodeTokens: 0, inputTokens: 0, cachedInputTokens: 0, outputTokens: 0 }
}

// Window-scoped fold with DSH's honesty rules: TTFT belongs to a turn's
// first sampled request, throughput sums only requests carrying both a first
// token and output tokens, and unsampled readings drop out instead of
// reading as zero. A turn without step traces contributes its lifecycle run
// trace when one was persisted.
export function foldTrajectoryStats(
  messages: ChatMessage[],
  lifecycleByTurn: ReadonlyMap<string, HandlersContextLifecycleTurn>,
): TrajectoryStats {
  const stats = emptyStats()
  let ttftSum = 0
  let ttftCount = 0
  for (const turn of messages) {
    if (turn.role !== 'assistant') continue
    stats.turns += 1
    const traces = turn.stepTraces ?? []
    if (traces.length > 0) {
      let firstTTFT: number | null = null
      for (const trace of traces) {
        stats.steps += 1
        stats.llmMs += Math.max(trace.ended_at_ms - trace.started_at_ms, 0)
        const usage = trace.usage
        stats.inputTokens += usage?.input_tokens ?? 0
        stats.cachedInputTokens += usage?.cached_input_tokens ?? 0
        stats.outputTokens += usage?.output_tokens ?? 0
        if (trace.first_token_at_ms && trace.first_token_at_ms >= trace.started_at_ms) {
          if (firstTTFT == null || trace.step_index < firstTTFT) {
            firstTTFT = trace.step_index
            ttftSum += trace.first_token_at_ms - trace.started_at_ms
            ttftCount += 1
          }
          const decode = trace.ended_at_ms - trace.first_token_at_ms
          if (decode > 0 && (usage?.output_tokens ?? 0) > 0) {
            stats.decodeMs += decode
            stats.decodeTokens += usage!.output_tokens!
          }
        }
      }
      for (const block of turn.messages) {
        if (block.type !== 'tool') continue
        const timing = toolTiming(block)
        if (!timing) continue
        stats.toolCalls += 1
        stats.toolMs += timing.end - timing.start
      }
      continue
    }
    const runTrace = turn.turnId ? lifecycleByTurn.get(turn.turnId)?.snapshot?.run_trace : undefined
    if (!runTrace) continue
    stats.steps += runTrace.steps ?? 0
    stats.toolCalls += runTrace.tool_calls ?? 0
    stats.llmMs += runTrace.llm_ms ?? 0
    stats.toolMs += runTrace.tool_ms ?? 0
    stats.decodeMs += runTrace.decode_ms ?? 0
    stats.decodeTokens += runTrace.decode_output_tokens ?? 0
    stats.inputTokens += runTrace.input_tokens ?? 0
    stats.cachedInputTokens += runTrace.cached_input_tokens ?? 0
    stats.outputTokens += runTrace.output_tokens ?? 0
    if ((runTrace.ttft_ms ?? 0) > 0) {
      ttftSum += runTrace.ttft_ms!
      ttftCount += 1
    }
  }
  // Only the turn's first request carried a TTFT above; one reading per turn.
  stats.ttftAvgMs = ttftCount > 0 ? Math.round(ttftSum / ttftCount) : null
  return stats
}

export interface VisibleRowRange {
  start: number
  end: number
  offsetTop: number
  totalHeight: number
}

export function visibleRowRange(input: { scrollTop: number, viewportHeight: number, rowHeight: number, count: number, overscan: number }): VisibleRowRange {
  const { rowHeight, count, overscan } = input
  if (count <= 0 || rowHeight <= 0) return { start: 0, end: 0, offsetTop: 0, totalHeight: 0 }
  const first = Math.floor(Math.max(input.scrollTop, 0) / rowHeight)
  const visible = Math.ceil(Math.max(input.viewportHeight, 0) / rowHeight)
  const start = Math.max(first - overscan, 0)
  const end = Math.min(first + visible + overscan, count)
  return { start, end, offsetTop: start * rowHeight, totalHeight: count * rowHeight }
}
