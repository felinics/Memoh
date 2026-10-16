import type { BotsBot } from '@memohai/sdk'

/**
 * The bots the switcher offers, in display order: pinned bots first (via
 * `sortBots`), then the user's saved drag order; bots missing from `order`
 * keep their pinned position at the end. A bot being deleted is left out —
 * the server removes it in the background and selecting it would point the
 * chat back at a bot that is going away.
 */
export function switcherBots(
  items: BotsBot[],
  sortBots: (bots: BotsBot[]) => BotsBot[],
  order: string[],
): BotsBot[] {
  const base = sortBots(items.filter(bot => bot.status !== 'deleting'))
  if (order.length === 0) return base
  const rank = new Map(order.map((id, index) => [id, index]))
  return [...base].sort((a, b) =>
    (rank.get(a.id ?? '') ?? Number.MAX_SAFE_INTEGER)
    - (rank.get(b.id ?? '') ?? Number.MAX_SAFE_INTEGER),
  )
}
