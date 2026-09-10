import type { Ref } from 'vue'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  getBotsByBotIdPackages,
  getBotsByBotIdPackagesByInstallationIdRemovalPreview,
  getSupermarketCategories,
  postBotsByBotIdPackagesByInstallationIdConnectorsByConnectorTypeApiKey,
  postBotsByBotIdPackagesByInstallationIdConnectorsByConnectorTypeOauth,
  postBotsByBotIdPackagesCheckUpdates,
  type HandlersPackageConnectorItem,
  type HandlersPackageDependencyItem,
  type HandlersPackageItem,
  type HandlersPackageListResponse,
  type HandlersPackageRemovalPreviewResponse,
  type HandlersPackageSkillItem,
  type HandlersSupermarketPackageCategory,
  type HandlersSupermarketPackageTranslation,
} from '@memohai/sdk'

// Domain aliases over the generated SDK types for the Package surfaces: the
// Supermarket, the bot's Packages tab, and the install / progress dialogs.
export type PackageItem = HandlersPackageItem
export type PackageListResponse = HandlersPackageListResponse
export type PackageDependencyItem = HandlersPackageDependencyItem
export type PackageConnectorItem = HandlersPackageConnectorItem
export type PackageSkillItem = HandlersPackageSkillItem
export type PackageStatus = NonNullable<PackageItem['status']>
export type PackageWorkspaceState = NonNullable<PackageListResponse['workspace_state']>
export type PackageRemovalPreview = HandlersPackageRemovalPreviewResponse
export type PackageCategory = HandlersSupermarketPackageCategory
export type PackageTranslations = Record<string, HandlersSupermarketPackageTranslation>

export const BOT_PACKAGES_QUERY_KEY = 'bot-packages'
export const PACKAGE_CATEGORIES_QUERY_KEY = 'supermarket-categories'

/**
 * Query key of one bot+target Package list. `invalidateBotPackages`
 * invalidates by the two-element prefix so every target of a bot refreshes.
 */
export function botPackagesQueryKey(botId: string, targetId: string): string[] {
  return [BOT_PACKAGES_QUERY_KEY, botId, targetId]
}

// The Server resolves an empty target to the bot's current one; an explicit
// id is only sent when the caller picked one.
function workspaceTargetQuery(targetId: string): { workspace_target_id: string } | undefined {
  const trimmed = targetId.trim()
  return trimmed ? { workspace_target_id: trimmed } : undefined
}

export function useBotPackagesQuery(botId: Ref<string>, targetId: Ref<string>, forceRefresh?: Ref<boolean>) {
  return useQuery({
    key: () => botPackagesQueryKey(botId.value, targetId.value),
    query: async () => {
      const refresh = forceRefresh?.value ?? false
      if (forceRefresh) forceRefresh.value = false
      const { data } = await getBotsByBotIdPackages({
        path: { bot_id: botId.value },
        query: { ...workspaceTargetQuery(targetId.value), refresh: refresh || undefined },
        throwOnError: true,
      })
      return data
    },
    enabled: () => !!botId.value,
  })
}

/** Refetches every target's Package list of one bot. */
export function invalidateBotPackages(
  queryCache: ReturnType<typeof useQueryCache>,
  botId: string,
): Promise<unknown> {
  return queryCache.invalidateQueries({ key: [BOT_PACKAGES_QUERY_KEY, botId] })
}

/**
 * Compares every installed Package with the registry and runs the dependency
 * update checks, returning the refreshed list.
 */
export async function checkPackageUpdates(botId: string, targetId: string): Promise<PackageListResponse> {
  const { data } = await postBotsByBotIdPackagesCheckUpdates({
    path: { bot_id: botId },
    query: workspaceTargetQuery(targetId),
    throwOnError: true,
  })
  return data
}

export async function fetchPackageRemovalPreview(botId: string, installationId: string): Promise<PackageRemovalPreview> {
  const { data } = await getBotsByBotIdPackagesByInstallationIdRemovalPreview({
    path: { bot_id: botId, installation_id: installationId },
    throwOnError: true,
  })
  return data
}

export async function beginPackageConnectorOAuth(botId: string, installationId: string, connectorType: string, authMethod: string) {
  const { data } = await postBotsByBotIdPackagesByInstallationIdConnectorsByConnectorTypeOauth({
    path: { bot_id: botId, installation_id: installationId, connector_type: connectorType },
    body: { auth_method: authMethod },
    throwOnError: true,
  })
  return data
}

export async function createPackageConnectorCredential(
  botId: string,
  installationId: string,
  connectorType: string,
  authMethod: string,
  fields: Record<string, string>,
) {
  const { data } = await postBotsByBotIdPackagesByInstallationIdConnectorsByConnectorTypeApiKey({
    path: { bot_id: botId, installation_id: installationId, connector_type: connectorType },
    body: { auth_method: authMethod, fields },
    throwOnError: true,
  })
  return data
}

/** The shared category table with localized names, across every enabled registry. */
export function usePackageCategoriesQuery() {
  return useQuery({
    key: () => [PACKAGE_CATEGORIES_QUERY_KEY],
    query: async () => {
      const { data } = await getSupermarketCategories({ throwOnError: true })
      return data.data ?? []
    },
  })
}

type PackageText = {
  name?: string
  description?: string
  package_id?: string
  translations?: PackageTranslations
}

function localeKey(language: string): string {
  return language.toLowerCase().split(/[-_]/)[0] ?? 'en'
}

/** Localized Package name: the translation for the UI language, else the manifest name. */
export function packageDisplayName(item: PackageText, language = 'en'): string {
  return item.translations?.[localeKey(language)]?.name?.trim()
    || item.name?.trim()
    || item.package_id?.trim()
    || ''
}

export function packageDisplayDescription(item: PackageText, language = 'en'): string {
  return item.translations?.[localeKey(language)]?.description?.trim()
    || item.description?.trim()
    || ''
}

/** Localized category name from the shared table, falling back to the Package's recorded English name. */
export function categoryDisplayName(category: Pick<PackageCategory, 'name' | 'names'> | undefined, fallback: string, language = 'en'): string {
  return category?.names?.[localeKey(language)]?.trim() || category?.name?.trim() || fallback
}

export function packageInProgress(item: Pick<PackageItem, 'status'>): boolean {
  return item.status === 'installing' || item.status === 'updating' || item.status === 'removing'
}

/** A newer release is known for this installation. */
export function packageUpdateAvailable(item: Pick<PackageItem, 'available_revision' | 'revision'>): boolean {
  const available = item.available_revision?.trim() ?? ''
  return !!available && available !== (item.revision ?? '')
}

/** Drives the tab count badge: Packages the user should act on. */
export function packageNeedsAttention(item: PackageItem): boolean {
  return item.status === 'partial' || item.status === 'failed' || packageUpdateAvailable(item)
}

/** Stable identity of a Package on a bot, independent of the installation record. */
export function packageKey(item: Pick<PackageItem, 'registry_id' | 'package_id'>): string {
  return `${item.registry_id ?? ''}/${item.package_id ?? ''}`
}
