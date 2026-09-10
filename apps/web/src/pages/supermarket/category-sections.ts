import type { HandlersSupermarketPackageCategory } from '@memohai/sdk'

/** Packages shown per category on the Supermarket front page before "View all". */
export const SECTION_PREVIEW_LIMIT = 8

/** Packages in the category, narrowed to one registry when `registryId` is set. */
export function categoryPackageCount(category: HandlersSupermarketPackageCategory, registryId = ''): number {
  if (!registryId) return category.package_count ?? 0
  return category.registries?.find(entry => entry.id === registryId)?.count ?? 0
}

/** Categories that get a section: non-empty for the registry, in table order. */
export function browsableCategories(
  categories: readonly HandlersSupermarketPackageCategory[],
  registryId = '',
): HandlersSupermarketPackageCategory[] {
  return categories
    .filter(category => categoryPackageCount(category, registryId) > 0)
    .sort((left, right) => (left.order ?? 0) - (right.order ?? 0) || left.id.localeCompare(right.id))
}
