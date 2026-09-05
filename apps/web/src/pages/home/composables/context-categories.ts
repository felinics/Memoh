import type { ContextfragKind, ContextfragKindBreakdown } from '@memohai/sdk'

export const CONTEXT_CATEGORY_IDS = ['system', 'rules', 'tools', 'skills', 'memory', 'summary', 'conversation', 'other'] as const
export type ContextCategoryId = (typeof CONTEXT_CATEGORY_IDS)[number]

export interface ContextCategoryStat {
  id: ContextCategoryId
  tokens: number
  colorClass: string
}

export interface ContextComposition {
  categories: ContextCategoryStat[]
  totalTokens: number
}

const CATEGORY_COLOR_CLASS: Record<ContextCategoryId, string> = {
  system: 'bg-accent-gray',
  rules: 'bg-accent-green',
  tools: 'bg-accent-purple',
  skills: 'bg-accent-yellow',
  memory: 'bg-accent-teal',
  summary: 'bg-accent-brown',
  conversation: 'bg-accent-orange',
  other: 'bg-accent-blue',
}

const KIND_CATEGORY: Record<ContextfragKind, ContextCategoryId> = {
  system_prompt: 'system',
  system_policy: 'system',
  bot_identity: 'system',
  platform_identity: 'system',
  workspace_instruction: 'rules',
  tool_usage: 'tools',
  tool_definition: 'tools',
  skills_catalog: 'skills',
  memory_recall: 'memory',
  conversation_summary: 'summary',
  conversation_event: 'conversation',
  current_user_message: 'conversation',
  attachment_ref: 'conversation',
  native_image: 'conversation',
  runtime_context: 'conversation',
  hook_context: 'other',
  injected_message: 'other',
  background_summary: 'other',
}

function categoryForKind(kind: ContextfragKind | undefined): ContextCategoryId {
  if (!kind) return 'other'
  return KIND_CATEGORY[kind] ?? 'other'
}

export interface ContextCompositionSource {
  breakdown?: ContextfragKindBreakdown[]
  tool_defs?: Array<{ token_estimate?: number }>
}

export function computeContextComposition(usage: ContextCompositionSource | null | undefined): ContextComposition | null {
  if (!usage) return null
  const breakdown = usage.breakdown ?? []
  const toolDefs = usage.tool_defs ?? []
  if (breakdown.length === 0 && toolDefs.length === 0) return null

  const tokensByCategory = new Map<ContextCategoryId, number>()
  for (const entry of breakdown) {
    const category = categoryForKind(entry.kind)
    tokensByCategory.set(category, (tokensByCategory.get(category) ?? 0) + (entry.token_estimate ?? 0))
  }
  const toolDefTokens = toolDefs.reduce((sum, bucket) => sum + (bucket.token_estimate ?? 0), 0)
  tokensByCategory.set('tools', (tokensByCategory.get('tools') ?? 0) + toolDefTokens)

  const categories: ContextCategoryStat[] = []
  let totalTokens = 0
  for (const id of CONTEXT_CATEGORY_IDS) {
    const tokens = tokensByCategory.get(id) ?? 0
    if (tokens <= 0) continue
    categories.push({ id, tokens, colorClass: CATEGORY_COLOR_CLASS[id] })
    totalTokens += tokens
  }
  if (categories.length === 0) return null

  return { categories, totalTokens }
}

// Every class is a literal so the Tailwind scanner can see it.
export function contextPressureToneClass(percent: number, kind: 'text' | 'bg'): string {
  if (percent >= 90) return kind === 'text' ? 'text-destructive' : 'bg-destructive'
  if (percent >= 70) return kind === 'text' ? 'text-warning' : 'bg-warning'
  return kind === 'text' ? 'text-foreground' : 'bg-foreground'
}

export function formatTokenCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

export function positive(value: number | null | undefined): number | null {
  return value != null && value > 0 ? value : null
}
