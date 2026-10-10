import type {
  BotCapability,
  BotCapabilitySnapshot,
  ChatExample,
  ChatExampleCategory,
  ChatExampleSurface,
  LocalizedText,
} from './types'

/** Resolve a locale map, matching a region locale such as `zh-CN` to `zh`; falls back to `en`. */
export function localizeText(text: LocalizedText, locale: string): string {
  const language = locale.toLowerCase().split('-')[0] as keyof LocalizedText
  return text[language] || text.en
}

/** Capabilities the bot is known to lack. Unknown capabilities never count as missing. */
export function missingCapabilities(example: ChatExample, snapshot: BotCapabilitySnapshot): BotCapability[] {
  return example.requires.filter(capability => snapshot[capability] === false)
}

export interface SelectChatExamplesOptions {
  surface: ChatExampleSurface
  capabilities: BotCapabilitySnapshot
  category?: ChatExampleCategory | 'all'
  limit?: number
  /** Drop examples with missing capabilities instead of sorting them last. */
  hideUnavailable?: boolean
  /** Prefer one example per category before repeating a category. */
  spreadCategories?: boolean
  /**
   * Shuffle instead of ranking by priority. The same seed always yields the
   * same order, so a surface stays stable while it re-renders.
   */
  seed?: number
}

/** Deterministic pseudo-random rank of an example for a seed (FNV-1a over seed + id). */
function seededRank(seed: number, id: string): number {
  let hash = 0x811C9DC5
  for (const char of `${seed}:${id}`) {
    hash ^= char.charCodeAt(0)
    hash = Math.imul(hash, 0x01000193)
  }
  return hash >>> 0
}

/**
 * Pick examples for a surface. Order: available before unavailable, then
 * priority (desc) — or a seeded shuffle when `seed` is set — then catalog
 * order. With `spreadCategories`, a first pass takes one example per
 * category in that order and a second pass fills the remaining slots.
 */
export function selectChatExamples(examples: readonly ChatExample[], options: SelectChatExamplesOptions): ChatExample[] {
  const category = options.category ?? 'all'
  const ranked = examples
    .map((example, index) => ({
      example,
      index,
      unavailable: missingCapabilities(example, options.capabilities).length > 0,
    }))
    .filter(({ example, unavailable }) =>
      example.surfaces.includes(options.surface)
      && (category === 'all' || example.category === category)
      && !(options.hideUnavailable && unavailable))
    .sort((a, b) => {
      const seed = options.seed
      const order = seed === undefined
        ? b.example.priority - a.example.priority
        : seededRank(seed, a.example.id) - seededRank(seed, b.example.id)
      return Number(a.unavailable) - Number(b.unavailable) || order || a.index - b.index
    })
    .map(({ example }) => example)

  const limit = options.limit ?? ranked.length
  if (!options.spreadCategories) return ranked.slice(0, limit)

  const picked: ChatExample[] = []
  const seenCategories = new Set<ChatExampleCategory>()
  for (const example of ranked) {
    if (picked.length >= limit) break
    if (seenCategories.has(example.category)) continue
    picked.push(example)
    seenCategories.add(example.category)
  }
  for (const example of ranked) {
    if (picked.length >= limit) break
    if (!picked.includes(example)) picked.push(example)
  }
  return picked
}
