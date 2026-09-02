import type {
  ContextfragLifecycleSnapshot,
  ContextfragSelectionDecision,
  ContextfragToolDefAccounting,
  HandlersContextLifecycleTurn,
} from '@memohai/sdk'
import { computeContextComposition, formatTokenCount, type ContextComposition } from './context-categories'

export function compositionFromSnapshot(snapshot: ContextfragLifecycleSnapshot | null | undefined): ContextComposition | null {
  return computeContextComposition({ breakdown: snapshot?.breakdown, tool_defs: snapshot?.tool_defs })
}

export interface DropReasonGroup {
  reason: string
  count: number
  tokens: number
}

export function groupDroppedDecisions(decisions: ContextfragSelectionDecision[] | null | undefined): DropReasonGroup[] {
  const groups = new Map<string, DropReasonGroup>()
  for (const entry of decisions ?? []) {
    if (entry.decision !== 'dropped') continue
    const reason = entry.reason?.trim() || 'unknown'
    const group = groups.get(reason) ?? { reason, count: 0, tokens: 0 }
    group.count += 1
    group.tokens += entry.token_estimate ?? 0
    groups.set(reason, group)
  }
  return [...groups.values()].sort((a, b) => b.tokens - a.tokens || b.count - a.count || a.reason.localeCompare(b.reason))
}

export function countTrimmed(decisions: ContextfragSelectionDecision[] | null | undefined): number {
  return (decisions ?? []).filter(entry => entry.decision === 'trimmed').length
}

// Every class is a literal so the Tailwind scanner can see it.
const STATUS_VIEW: Record<string, { tone: string, labelKey: string }> = {
  completed: { tone: 'text-muted-foreground', labelKey: 'chat.lifecycle.statusCompleted' },
  fallback: { tone: 'text-warning', labelKey: 'chat.lifecycle.statusFallback' },
  failed_budget: { tone: 'text-destructive', labelKey: 'chat.lifecycle.statusFailedBudget' },
  failed_provider: { tone: 'text-destructive', labelKey: 'chat.lifecycle.statusFailedProvider' },
  aborted: { tone: 'text-destructive', labelKey: 'chat.lifecycle.statusAborted' },
}

export function lifecycleStatusToneClass(status: string | null | undefined): string {
  return STATUS_VIEW[status ?? '']?.tone ?? 'text-muted-foreground'
}

export function lifecycleStatusLabelKey(status: string | null | undefined): string | null {
  return STATUS_VIEW[status ?? '']?.labelKey ?? null
}

export type PromptDiff = 'initial' | 'tools' | 'system' | 'history'

function toolRoster(defs: ContextfragToolDefAccounting[] | undefined): string {
  return (defs ?? []).map(def => `${def.provider ?? ''}/${def.name ?? ''}:${def.bytes ?? 0}`).sort().join('|')
}

// `previous` is null for the first turn of a session and undefined when the
// older turn is unknown (page boundary), where no honest label exists.
export function classifyPromptDiff(
  current: ContextfragLifecycleSnapshot,
  previous: ContextfragLifecycleSnapshot | null | undefined,
): PromptDiff | null {
  if (previous === null) return 'initial'
  if (previous === undefined) return null
  if (toolRoster(current.tool_defs) !== toolRoster(previous.tool_defs)) return 'tools'
  if (!current.stable_prefix_hash || !previous.stable_prefix_hash) return null
  return current.stable_prefix_hash === previous.stable_prefix_hash ? 'history' : 'system'
}

const DIFF_LABEL_KEY: Record<PromptDiff, string> = {
  initial: 'chat.lifecycle.diffInitial',
  tools: 'chat.lifecycle.diffTools',
  system: 'chat.lifecycle.diffSystem',
  history: 'chat.lifecycle.diffHistory',
}

const TRUST_LABEL_KEY: Record<string, string> = {
  system: 'chat.lifecycle.trustSystem',
  workspace: 'chat.lifecycle.trustWorkspace',
  user: 'chat.lifecycle.trustUser',
  external: 'chat.lifecycle.trustExternal',
}

export interface LabeledValue {
  key: string
  label: string
  value: string
  mono?: boolean
}

export interface TurnSection {
  key: string
  testId: string
  titleKey: string
  rows: LabeledValue[]
}

export interface TurnRow {
  key: string
  runId: string
  time: string
  model: string
  statusLabel: string
  statusTone: string
  total: string
  diffKey: string | null
  composition: ContextComposition | null
  contextWindow: number | null
  outputReserve: number | null
  selection: string
  metrics: LabeledValue[]
  sections: TurnSection[]
  detailPending: boolean
}

type Translate = (key: string, params?: Record<string, unknown>) => string

export interface BuildTurnRowOptions {
  t: Translate
  formatTime: (iso: string | undefined) => string
  index?: number
  detail?: ContextfragLifecycleSnapshot
  previous?: ContextfragLifecycleSnapshot | null
  detailPending?: boolean
}

function positive(value: number | undefined): number | null {
  return value != null && value > 0 ? value : null
}

function section(key: string, testId: string, titleKey: string, rows: LabeledValue[]): TurnSection[] {
  return rows.length ? [{ key, testId, titleKey, rows }] : []
}

// A lone clean step is the normal shape and says nothing; the block only earns
// its space once the loop re-ran or a step lost fragments.
function stepRows(snapshot: ContextfragLifecycleSnapshot, t: Translate): LabeledValue[] {
  const steps = snapshot.steps ?? []
  const noteworthy = steps.length > 1
    || steps.some(step => (step.dropped ?? 0) > 0 || (step.truncated ?? 0) > 0 || step.reselection_applied === true)
  if (!noteworthy) return []
  return steps.map((step, index) => ({
    key: `${step.step_index ?? index}-${step.attempt ?? 0}`,
    label: `#${step.step_index ?? index}`,
    mono: true,
    value: [
      (step.dropped ?? 0) > 0 ? t('chat.lifecycle.droppedCount', { n: step.dropped }) : '',
      (step.truncated ?? 0) > 0 ? t('chat.lifecycle.truncatedCount', { n: step.truncated }) : '',
      step.reselection_outcome?.trim() ?? '',
    ].filter(Boolean).join(' · '),
  }))
}

export function buildTurnRow(turn: HandlersContextLifecycleTurn, options: BuildTurnRowOptions): TurnRow {
  const { t, detail, index = 0 } = options
  const snapshot = turn.snapshot ?? {}
  const decisions = detail?.selection_decisions
  const composition = compositionFromSnapshot(snapshot)
  const statusKey = lifecycleStatusLabelKey(turn.status)
  const diff = classifyPromptDiff(snapshot, options.previous)

  const selection = snapshot.selection
    ? [
        t('chat.lifecycle.selectedCount', { n: snapshot.selection.selected ?? 0 }),
        t('chat.lifecycle.droppedCount', { n: snapshot.selection.dropped ?? 0 }),
        ...(countTrimmed(decisions) > 0 ? [t('chat.lifecycle.trimmedCount', { n: countTrimmed(decisions) })] : []),
      ].join(' · ')
    : ''

  const metrics: [string, string, number | undefined][] = [
    ['window', 'chat.lifecycle.window', snapshot.budget_plan?.window],
    ['stablePrefix', 'chat.lifecycle.stablePrefix', snapshot.stable_prefix_token_estimate],
    ['cacheRead', 'chat.lifecycle.cacheRead', snapshot.cache_read_tokens],
    ['cacheWrite', 'chat.lifecycle.cacheWrite', snapshot.cache_write_tokens],
  ]

  return {
    key: turn.run_id || turn.assistant_message_id || `turn-${index}`,
    runId: turn.run_id ?? '',
    time: options.formatTime(turn.created_at),
    model: snapshot.model ?? '',
    statusLabel: statusKey ? t(statusKey) : turn.status ?? '',
    statusTone: lifecycleStatusToneClass(turn.status),
    total: formatTokenCount(composition?.totalTokens ?? snapshot.counts?.token_estimate ?? 0),
    diffKey: diff ? DIFF_LABEL_KEY[diff] : null,
    composition,
    contextWindow: positive(snapshot.budget_plan?.window),
    outputReserve: positive(snapshot.budget_plan?.output_reserve),
    selection,
    metrics: metrics.flatMap(([key, labelKey, value]) => {
      const tokens = positive(value)
      return tokens == null ? [] : [{ key, label: t(labelKey), value: formatTokenCount(tokens) }]
    }),
    sections: [
      ...section('dropReasons', 'drop-reason', 'chat.lifecycle.dropReasons', groupDroppedDecisions(decisions).map(group => ({
        key: group.reason,
        label: group.reason === 'unknown' ? t('chat.lifecycle.unknown') : group.reason,
        value: `${group.count} · ${formatTokenCount(group.tokens)}`,
      }))),
      ...section('trust', 'trust', 'chat.lifecycle.trust', (snapshot.trust_breakdown ?? []).map((entry, i) => {
        const trust = entry.trust ?? ''
        const labelKey = TRUST_LABEL_KEY[trust]
        return {
          key: trust || `trust-${i}`,
          label: labelKey ? t(labelKey) : trust || t('chat.lifecycle.unknown'),
          value: formatTokenCount(entry.token_estimate ?? 0),
        }
      })),
      ...section('mutations', 'mutation', 'chat.lifecycle.mutations', (snapshot.mutations ?? []).flatMap((mutation, i) => {
        const kind = mutation.kind?.trim() ?? ''
        return kind ? [{ key: `${kind}-${i}`, label: kind, mono: true, value: mutation.detail?.trim() ?? '' }] : []
      })),
      ...section('steps', 'step', 'chat.lifecycle.steps', stepRows(snapshot, t)),
    ],
    detailPending: options.detailPending === true,
  }
}
