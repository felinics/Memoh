import { useI18n } from 'vue-i18n'
import type { DependencyItem } from '@/composables/api/useWorkspaceDependencies'
import { dependencyText } from '@/utils/workspace-dependency'
import { sdkApiUrl } from '@/lib/api-client'

type CatalogText = Pick<DependencyItem, 'id' | 'name' | 'description' | 'translations' | 'icon_url'>

export function useWorkspaceDependencyText() {
  const { locale } = useI18n()
  return {
    dependencyName: (item: CatalogText) => dependencyText(item, 'name', locale.value),
    dependencyDescription: (item: CatalogText) => dependencyText(item, 'description', locale.value),
    dependencyIconUrl: (item: CatalogText) => {
      const path = item.icon_url ?? ''
      return /^\/workspace-dependencies\/icons\/[a-f0-9]{64}$/.test(path) ? sdkApiUrl({ url: path }) : ''
    },
  }
}
