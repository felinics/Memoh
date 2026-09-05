import type {
  CompactionLog,
  ContextfragKind,
  ContextfragLifecycleSnapshot,
  ContextfragMemoryRecallTrace,
  ContextfragMutationRecord,
  ContextfragSelectionTrace,
  ContextfragStepSnapshot,
  HandlersContextLifecycleTurn,
} from '@memohai/sdk'
import type { UIStepTrace } from '@/composables/api/useChat.types'
import type { ChatAssistantTurn, ChatMessage, ChatUserTurn, ContentBlock, ToolCallBlock } from '@/store/chat/types'

export const PREVIEW_SOURCE_CHARACTERS = 2048
export const PREVIEW_OUTPUT_CHARACTERS = 512

export type TrajectoryRowKind = 'system' | 'user' | 'context' | 'assistant' | 'reasoning' | 'tool' | 'error' | 'notice' | 'compaction'

// What the turn's context was assembled from, read off the persisted
// lifecycle manifest: counts and token estimates per fragment kind, never
// prompt text.
// One injected fragment as the run recorded it; its text and name live in the
// content-addressed store under textHash, which is empty when the run stored
// no text for it. Only tool definitions carry a name of their own, from the
// accounting the snapshot keeps.
export interface FragmentRef {
  id: string
  kind: string
  textHash: string
  tokens: number
  bytes: number
}

export type ContextEntry =
  | { kind: 'fragments', fragmentKind: ContextfragKind, fragments: number, tokens: number, textBytes: number, images: number, refs: FragmentRef[], memory?: ContextfragMemoryRecallTrace, selection?: ContextfragSelectionTrace }
  | { kind: 'memory_recall', memory: ContextfragMemoryRecallTrace }
  | { kind: 'tool_defs', tools: number, tokens: number, providers: string[], refs: FragmentRef[] }
  | { kind: 'selection', selection: ContextfragSelectionTrace }
  | { kind: 'mutation', mutation: ContextfragMutationRecord }
  | { kind: 'step', step: ContextfragStepSnapshot }

export interface SystemEntry {
  fragments: number
  tokens: number
  refs: FragmentRef[]
}

export function entryRefs(entry: SystemEntry | ContextEntry): FragmentRef[] {
  if ('refs' in entry) return entry.refs
  return []
}

export interface ContextEntries {
  system: SystemEntry | null
  before: ContextEntry[]
  perStep: Map<number, ContextEntry[]>
}

export type TrajectoryDetail =
  | { kind: 'user', turn: ChatUserTurn }
  // previous is the run before this one in the session: null for the first
  // run, undefined when the older run is not loaded.
  | { kind: 'system', lifecycle: HandlersContextLifecycleTurn, entry: SystemEntry, previous: HandlersContextLifecycleTurn | null | undefined }
  | { kind: 'context', lifecycle: HandlersContextLifecycleTurn, entry: ContextEntry }
  | { kind: 'block', turn: ChatAssistantTurn, block: ContentBlock, trace: UIStepTrace | null }
  | { kind: 'compaction', compaction: CompactionLog }

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

// One segment of the strip above the ledger: a ledger row on its lane, or one
// model request folded from the reasoning and text rows it produced.
export interface RowMapSegment {
  key: string
  rowKey: string
  lane: TimelineLane
  kind: TrajectoryRowKind
  turnId: string
  turnStart: boolean
  durationMs: number
  splitMs: number | null
  label: string
  running: boolean
  stepIndex: number | null
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

// The run before each run in a newest-first lifecycle list: null for the
// oldest run of a complete list, undefined for the oldest of a partial one.
export function previousLifecycleByRun(turns: HandlersContextLifecycleTurn[], hasOlder: boolean): Map<string, HandlersContextLifecycleTurn | null | undefined> {
  const previous = new Map<string, HandlersContextLifecycleTurn | null | undefined>()
  turns.forEach((turn, index) => {
    if (!turn.run_id) return
    previous.set(turn.run_id, turns[index + 1] ?? (hasOlder ? undefined : null))
  })
  return previous
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
  // Whitespace can only shrink the text, so a bounded slice is enough input
  // for a bounded preview; the full body is never scanned.
  const flat = text.slice(0, limit * 4).replace(/\s+/g, ' ').trim()
  return flat.length > limit ? `${flat.slice(0, Math.max(limit - 1, 0))}…` : flat
}

const SYSTEM_KINDS = new Set<ContextfragKind>(['system_prompt', 'system_policy', 'bot_identity', 'platform_identity'])

function stepChangedContext(step: ContextfragStepSnapshot): boolean {
  return (step.dropped ?? 0) > 0 || (step.truncated ?? 0) > 0 || step.reselection_applied === true
}

export function contextEntries(snapshot: ContextfragLifecycleSnapshot | null | undefined): ContextEntries {
  const before: ContextEntry[] = []
  const perStep = new Map<number, ContextEntry[]>()
  if (!snapshot) return { system: null, before, perStep }
  const refsByKind = new Map<string, FragmentRef[]>()
  for (const ref of snapshot.fragments ?? []) {
    const kind = ref.kind ?? ''
    if (!kind) continue
    const list = refsByKind.get(kind) ?? []
    list.push({ id: '', kind, textHash: ref.text_hash ?? '', tokens: ref.token_estimate ?? 0, bytes: ref.text_bytes ?? 0 })
    refsByKind.set(kind, list)
  }
  let system: SystemEntry | null = null
  let memoryInjected = false
  for (const entry of snapshot.breakdown ?? []) {
    const kind = entry.kind
    if (!kind || kind === 'current_user_message') continue
    const fragments = entry.fragments ?? 0
    const tokens = entry.token_estimate ?? 0
    const refs = refsByKind.get(kind) ?? []
    if (SYSTEM_KINDS.has(kind)) {
      system = system
        ? { fragments: system.fragments + fragments, tokens: system.tokens + tokens, refs: [...system.refs, ...refs] }
        : { fragments, tokens, refs }
      continue
    }
    const row: ContextEntry = { kind: 'fragments', fragmentKind: kind, fragments, tokens, textBytes: entry.text_bytes ?? 0, images: entry.images ?? 0, refs }
    if (kind === 'memory_recall' && snapshot.memory_recall) {
      row.memory = snapshot.memory_recall
      memoryInjected = true
    }
    if (kind === 'conversation_event' && snapshot.selection) row.selection = snapshot.selection
    before.push(row)
  }
  if (snapshot.memory_recall && !memoryInjected) before.push({ kind: 'memory_recall', memory: snapshot.memory_recall })
  const toolDefs = snapshot.tool_defs ?? []
  if (toolDefs.length > 0) {
    before.push({
      kind: 'tool_defs',
      tools: toolDefs.length,
      tokens: toolDefs.reduce((sum, def) => sum + (def.token_estimate ?? 0), 0),
      providers: [...new Set(toolDefs.map(def => def.provider ?? '').filter(Boolean))].sort(),
      refs: toolDefs.map(def => ({ id: `${def.provider ?? ''}/${def.name ?? ''}`, kind: 'tool_definition', textHash: def.content_hash ?? '', tokens: def.token_estimate ?? 0, bytes: def.bytes ?? 0 })),
    })
  }
  const selection = snapshot.selection
  if (selection && (selection.dropped ?? 0) + (selection.trimmed ?? 0) > 0) before.push({ kind: 'selection', selection })
  for (const mutation of snapshot.mutations ?? []) {
    if (mutation.kind) before.push({ kind: 'mutation', mutation })
  }
  for (const step of snapshot.steps ?? []) {
    if (step.step_index == null || !stepChangedContext(step)) continue
    const rows = perStep.get(step.step_index) ?? []
    rows.push({ kind: 'step', step })
    perStep.set(step.step_index, rows)
  }
  return { system, before, perStep }
}

function traceForBlock(traces: UIStepTrace[] | undefined, blockId: number): UIStepTrace | null {
  if (!traces?.length) return null
  for (const trace of traces) {
    const last = trace.last_message_id ?? Number.POSITIVE_INFINITY
    if (trace.first_message_id <= blockId && blockId <= last) return trace
  }
  return null
}

// A block belongs to the finished request whose anchor range contains it;
// blocks streamed after the last finished request stay untimed.
export function stepIndexForBlock(traces: UIStepTrace[] | undefined, blockId: number): number | null {
  return traceForBlock(traces, blockId)?.step_index ?? null
}

function toolTiming(block: ToolCallBlock): { start: number, end: number } | null {
  const timing = block.execution_timing
  if (!timing || timing.started_at_ms <= 0 || timing.ended_at_ms < timing.started_at_ms) return null
  return { start: timing.started_at_ms, end: timing.ended_at_ms }
}

function contextLabel(entry: ContextEntry): string {
  switch (entry.kind) {
    case 'fragments':
      return entry.fragmentKind
    case 'mutation':
      return entry.mutation.kind ?? ''
    default:
      return entry.kind
  }
}

function contextRow(lifecycle: HandlersContextLifecycleTurn, entry: ContextEntry, key: string, turnId: string, turnLabel: string, stepIndex: number | null): TrajectoryRow {
  return {
    key,
    kind: 'context',
    turnId,
    turnLabel,
    turnStart: false,
    stepIndex,
    label: contextLabel(entry),
    preview: '',
    output: null,
    startedAtMs: null,
    endedAtMs: null,
    running: false,
    detail: { kind: 'context', lifecycle, entry },
  }
}

// The rows a turn's context assembly contributes ahead of its first request:
// the system prompt, then everything else the manifest injected.
function turnContextRows(lifecycle: HandlersContextLifecycleTurn, entries: ContextEntries, turnId: string, turnLabel: string, previous: HandlersContextLifecycleTurn | null | undefined): { system: TrajectoryRow | null, before: TrajectoryRow[] } {
  const runId = lifecycle.run_id ?? turnId
  const system: TrajectoryRow | null = entries.system
    ? {
        key: `${runId}:system`,
        kind: 'system',
        turnId,
        turnLabel,
        turnStart: false,
        stepIndex: null,
        label: '',
        preview: '',
        output: null,
        startedAtMs: null,
        endedAtMs: null,
        running: false,
        detail: { kind: 'system', lifecycle, entry: entries.system, previous },
      }
    : null
  const before = entries.before.map((entry, index) => contextRow(lifecycle, entry, `${runId}:context:${index}`, turnId, turnLabel, null))
  return { system, before }
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

function userRow(turn: ChatUserTurn, turnLabel: string): TrajectoryRow {
  const injection = turn.contextInjection?.kind?.trim() ?? ''
  return {
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
}

// Block rows of one assistant turn; a step that re-selected its context gets
// that change listed before the first block the step produced. A turn
// continued by a later run (a tool approval, an answered question) counts
// its steps from zero again, so a step index can recur: the lifecycle's
// step context belongs to its first occurrence, and later ones only keep
// their rows apart.
function assistantRows(turn: ChatAssistantTurn, lifecycle: HandlersContextLifecycleTurn | undefined, perStep: ReadonlyMap<number, ContextEntry[]>, turnLabel: string): TrajectoryRow[] {
  const turnRows: TrajectoryRow[] = []
  const turnId = turn.turnId ?? ''
  const runId = lifecycle?.run_id ?? turnId
  let openStep: number | null = null
  const seenSteps = new Set<number>()
  for (const block of turn.messages) {
    const row = blockRow(turn, block, turnLabel)
    if (!row) continue
    if (row.stepIndex != null && row.stepIndex !== openStep) {
      openStep = row.stepIndex
      if (!seenSteps.has(row.stepIndex)) {
        seenSteps.add(row.stepIndex)
        const entries = lifecycle ? perStep.get(row.stepIndex) ?? [] : []
        entries.forEach((entry, index) => {
          turnRows.push(contextRow(lifecycle!, entry, `${runId}:step:${row.stepIndex}:${index}`, turnId, turnLabel, row.stepIndex))
        })
      }
    }
    turnRows.push(row)
  }
  return turnRows
}

// The signature captures everything a settled turn's rows depend on. Every
// block contributes, not only the tail: tools run in parallel, so an earlier
// tool can finish while a later one is still streaming.
function blockSignature(block: ContentBlock): string {
  if (block.type === 'tool') {
    return `${block.id}:t:${block.running ? 1 : 0}:${block.result == null && block.output == null ? 0 : 1}:${block.execution_timing ? 1 : 0}`
  }
  return `${block.id}:${block.type}:${'content' in block ? block.content.length : ''}`
}

function assistantSignature(turn: ChatAssistantTurn, lifecycle: HandlersContextLifecycleTurn | undefined, turnLabel: string): string {
  return `${turnLabel}|${turn.turnId ?? ''}|${turn.messages.map(blockSignature).join(',')}|${turn.stepTraces?.length ?? 0}|${lifecycle?.run_id ?? ''}|${turn.streaming}`
}

export type TrajectoryRowBuilder = (
  messages: ChatMessage[],
  lifecycleByTurn: ReadonlyMap<string, HandlersContextLifecycleTurn>,
  previousByRun?: ReadonlyMap<string, HandlersContextLifecycleTurn | null | undefined>,
  compactions?: readonly CompactionLog[],
) => TrajectoryRow[]

// A compaction is a model request of its own: it sits between the turns it
// ran between, on the turn it followed, and reads like DSH's compacted row.
function compactionRow(compaction: CompactionLog, turnId: string, turnLabel: string): TrajectoryRow {
  const started = Date.parse(compaction.started_at ?? '')
  const ended = compaction.completed_at ? Date.parse(compaction.completed_at) : Number.NaN
  return {
    key: `compaction:${compaction.id ?? started}`,
    kind: 'compaction',
    turnId,
    turnLabel,
    turnStart: false,
    stepIndex: null,
    label: compaction.status ?? '',
    preview: previewText(compaction.summary, PREVIEW_OUTPUT_CHARACTERS),
    output: null,
    startedAtMs: Number.isFinite(started) ? started : null,
    endedAtMs: Number.isFinite(ended) ? ended : null,
    running: compaction.status === 'pending',
    detail: { kind: 'compaction', compaction },
  }
}

function compactionStartMs(compaction: CompactionLog): number {
  return Date.parse(compaction.started_at ?? '')
}

// Rows of unchanged assistant turns are reused across rebuilds, so a streamed
// token costs the rows of one turn, not the whole loaded window. A turn's
// context rows are built once per persisted lifecycle and lead the turn
// whether its user message or only its assistant output is loaded.
export function createTrajectoryRowBuilder(): TrajectoryRowBuilder {
  const assistantCache = new WeakMap<ChatAssistantTurn, { signature: string, rows: TrajectoryRow[] }>()
  const contextCache = new WeakMap<HandlersContextLifecycleTurn, { turnLabel: string, previous: HandlersContextLifecycleTurn | null | undefined, entries: ContextEntries, system: TrajectoryRow | null, before: TrajectoryRow[] }>()
  const contextOf = (lifecycle: HandlersContextLifecycleTurn, turnId: string, turnLabel: string, previous: HandlersContextLifecycleTurn | null | undefined) => {
    const cached = contextCache.get(lifecycle)
    if (cached && cached.turnLabel === turnLabel && cached.previous === previous) return cached
    const entries = contextEntries(lifecycle.snapshot)
    const built = { turnLabel, previous, entries, ...turnContextRows(lifecycle, entries, turnId, turnLabel, previous) }
    contextCache.set(lifecycle, built)
    return built
  }
  return (messages, lifecycleByTurn, previousByRun, compactions) => {
    const rows: TrajectoryRow[] = []
    let lastTurnKey: string | null = null
    let lastTurnId = ''
    let lastTurnLabel = ''
    let position = 0
    // Compactions older than the loaded window belong to turns that are not
    // loaded; the rest interleave by the time they ran.
    const pending = compactions ?? []
    let nextCompaction = 0
    for (const turn of messages) {
      if (turn.role === 'system') continue
      const turnKey = turn.turnId || turn.id
      const turnStart = turnKey !== lastTurnKey
      if (turnStart) {
        const at = Date.parse(turn.timestamp)
        while (nextCompaction < pending.length && !(compactionStartMs(pending[nextCompaction]!) > at)) {
          if (lastTurnKey !== null) rows.push(compactionRow(pending[nextCompaction]!, lastTurnId, lastTurnLabel))
          nextCompaction += 1
        }
        position = turn.turnPosition ?? position + 1
        lastTurnKey = turnKey
      }
      const turnLabel = String(turn.turnPosition ?? position)
      lastTurnId = turn.turnId ?? ''
      lastTurnLabel = turnLabel
      const lifecycle = turn.turnId ? lifecycleByTurn.get(turn.turnId) : undefined
      const previous = lifecycle?.run_id ? previousByRun?.get(lifecycle.run_id) : undefined
      const context = lifecycle && turnStart ? contextOf(lifecycle, turn.turnId ?? '', turnLabel, previous) : null
      let turnRows: TrajectoryRow[]
      if (turn.role === 'user') {
        turnRows = context ? [...(context.system ? [context.system] : []), userRow(turn, turnLabel), ...context.before] : [userRow(turn, turnLabel)]
      } else {
        const perStep = lifecycle ? contextOf(lifecycle, turn.turnId ?? '', turnLabel, previous).entries.perStep : new Map<number, ContextEntry[]>()
        const signature = assistantSignature(turn, lifecycle, turnLabel)
        const cached = assistantCache.get(turn)
        let blocks: TrajectoryRow[]
        if (cached && cached.signature === signature) {
          blocks = cached.rows
        } else {
          blocks = assistantRows(turn, lifecycle, perStep, turnLabel)
          assistantCache.set(turn, { signature, rows: blocks })
        }
        turnRows = context ? [...(context.system ? [context.system] : []), ...context.before, ...blocks] : blocks
      }
      if (turnRows.length === 0) continue
      const first = turnRows[0]!
      if (first.turnStart !== turnStart) {
        turnRows = [{ ...first, turnStart }, ...turnRows.slice(1)]
      }
      rows.push(...turnRows)
    }
    if (lastTurnKey !== null) {
      for (; nextCompaction < pending.length; nextCompaction += 1) {
        rows.push(compactionRow(pending[nextCompaction]!, lastTurnId, lastTurnLabel))
      }
    }
    return rows
  }
}

export function buildTrajectoryRows(
  messages: ChatMessage[],
  lifecycleByTurn: ReadonlyMap<string, HandlersContextLifecycleTurn>,
  previousByRun?: ReadonlyMap<string, HandlersContextLifecycleTurn | null | undefined>,
  compactions?: readonly CompactionLog[],
): TrajectoryRow[] {
  return createTrajectoryRowBuilder()(messages, lifecycleByTurn, previousByRun, compactions)
}

// DSH's lane rule: tools on their own lane, model output and compactions on
// the model lane split at the first token, everything else the context
// received on the input lane. The reasoning and text a single request
// streamed fold into one model segment; blocks still streaming after the
// last finished request stay their own untimed segment.
export function buildRowMap(rows: TrajectoryRow[]): RowMapSegment[] {
  const segments: RowMapSegment[] = []
  let open: RowMapSegment | null = null
  for (const row of rows) {
    if (row.kind === 'compaction') {
      open = null
      segments.push({
        key: `map:${row.key}`,
        rowKey: row.key,
        lane: 'model',
        kind: row.kind,
        turnId: row.turnId,
        turnStart: false,
        durationMs: row.startedAtMs != null && row.endedAtMs != null ? Math.max(row.endedAtMs - row.startedAtMs, 0) : 0,
        splitMs: null,
        label: row.label,
        running: row.running,
        stepIndex: null,
      })
      continue
    }
    if ((row.kind === 'reasoning' || row.kind === 'assistant') && row.detail.kind === 'block') {
      const trace = row.detail.trace
      if (trace && open && open.turnId === row.turnId && open.stepIndex === trace.step_index) continue
      const duration = trace ? Math.max(trace.ended_at_ms - trace.started_at_ms, 0) : 0
      const sampled = trace?.first_token_at_ms != null && trace.first_token_at_ms >= trace.started_at_ms && trace.first_token_at_ms <= trace.ended_at_ms
      const segment: RowMapSegment = {
        key: `map:${row.key}`,
        rowKey: row.key,
        lane: 'model',
        kind: row.kind,
        turnId: row.turnId,
        turnStart: row.turnStart,
        durationMs: duration,
        splitMs: trace && sampled ? trace.first_token_at_ms! - trace.started_at_ms : null,
        label: trace?.finish_reason ?? '',
        running: !trace && row.detail.turn.streaming,
        stepIndex: trace?.step_index ?? null,
      }
      segments.push(segment)
      open = trace ? segment : null
      continue
    }
    open = null
    segments.push({
      key: `map:${row.key}`,
      rowKey: row.key,
      lane: row.kind === 'tool' ? 'tools' : 'input',
      kind: row.kind,
      turnId: row.turnId,
      turnStart: row.turnStart,
      durationMs: row.startedAtMs != null && row.endedAtMs != null ? Math.max(row.endedAtMs - row.startedAtMs, 0) : 0,
      splitMs: null,
      label: row.label,
      running: row.running,
      stepIndex: row.stepIndex,
    })
  }
  return segments
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
      let firstSampled: UIStepTrace | null = null
      for (const trace of traces) {
        stats.steps += 1
        stats.llmMs += Math.max(trace.ended_at_ms - trace.started_at_ms, 0)
        const usage = trace.usage
        stats.inputTokens += usage?.input_tokens ?? 0
        stats.cachedInputTokens += usage?.cached_input_tokens ?? 0
        stats.outputTokens += usage?.output_tokens ?? 0
        if (trace.first_token_at_ms && trace.first_token_at_ms >= trace.started_at_ms) {
          if (!firstSampled || trace.step_index < firstSampled.step_index) firstSampled = trace
          const decode = trace.ended_at_ms - trace.first_token_at_ms
          if (decode > 0 && (usage?.output_tokens ?? 0) > 0) {
            stats.decodeMs += decode
            stats.decodeTokens += usage!.output_tokens!
          }
        }
      }
      if (firstSampled) {
        ttftSum += firstSampled.first_token_at_ms! - firstSampled.started_at_ms
        ttftCount += 1
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
