import { useI18n } from 'vue-i18n'
import type { HandlersWorkspaceDependencyCatalogItem } from '@memohai/sdk'
import type { DependencyItem } from '@/composables/api/useWorkspaceDependencies'
import { dependencyDisplayName } from '@/utils/workspace-dependency'

// Localized name and description of a catalog dependency. The Server's catalog
// speaks English; `bots.dependencies.catalog.<id>.{name,description}` carries
// the translation when one exists, and the Server text is the fallback so a
// dependency the locale files do not know still reads correctly.

type CatalogText = Pick<DependencyItem | HandlersWorkspaceDependencyCatalogItem, 'id' | 'name' | 'description'>

export function useWorkspaceDependencyText() {
  const { t, te } = useI18n()

  function localized(item: CatalogText, field: 'name' | 'description', fallback: string): string {
    const id = item.id?.trim()
    if (!id) return fallback
    const key = `bots.dependencies.catalog.${id}.${field}`
    return te(key) ? t(key) : fallback
  }

  return {
    dependencyName: (item: CatalogText) => localized(item, 'name', dependencyDisplayName(item)),
    dependencyDescription: (item: CatalogText) => localized(item, 'description', (item.description ?? '').trim()),
  }
}
