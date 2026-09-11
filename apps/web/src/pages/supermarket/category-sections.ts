import type { HandlersSupermarketAppCategory } from '@memohai/sdk'

/** Apps shown per category on the Supermarket front page before "View all". */
export const SECTION_PREVIEW_LIMIT = 8

/** Apps in the category, narrowed to one registry when `registryId` is set. */
export function categoryAppCount(category: HandlersSupermarketAppCategory, registryId = ''): number {
  if (!registryId) return category.app_count ?? 0
  return category.registries?.find(entry => entry.id === registryId)?.count ?? 0
}

/** Categories that get a section: non-empty for the registry, in table order. */
export function browsableCategories(
  categories: readonly HandlersSupermarketAppCategory[],
  registryId = '',
): HandlersSupermarketAppCategory[] {
  return categories
    .filter(category => categoryAppCount(category, registryId) > 0)
    .sort((left, right) => (left.order ?? 0) - (right.order ?? 0) || left.id.localeCompare(right.id))
}
