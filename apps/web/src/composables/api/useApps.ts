import type { Ref } from 'vue'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  getBotsByBotIdApps,
  getBotsByBotIdAppsByInstallationIdRemovalPreview,
  getSupermarketCategories,
  postBotsByBotIdAppsByInstallationIdConnectorsByConnectorTypeApiKey,
  postBotsByBotIdAppsByInstallationIdConnectorsByConnectorTypeOauth,
  postBotsByBotIdAppsCheckUpdates,
  type HandlersAppConnectorItem,
  type HandlersAppDependencyItem,
  type HandlersAppItem,
  type HandlersAppListResponse,
  type HandlersAppRemovalPreviewResponse,
  type HandlersAppSkillItem,
  type HandlersSupermarketAppCategory,
  type HandlersSupermarketAppTranslation,
} from '@memohai/sdk'
import { dependencyUpdateAvailable } from '@/utils/workspace-dependency'

// Domain aliases over the generated SDK types for the App surfaces: the
// Supermarket, the bot's Apps tab, and the install / progress dialogs.
export type AppItem = HandlersAppItem
export type AppListResponse = HandlersAppListResponse
export type AppDependencyItem = HandlersAppDependencyItem
export type AppConnectorItem = HandlersAppConnectorItem
export type AppSkillItem = HandlersAppSkillItem
export type AppStatus = NonNullable<AppItem['status']>
export type AppWorkspaceState = NonNullable<AppListResponse['workspace_state']>
export type AppRemovalPreview = HandlersAppRemovalPreviewResponse
export type AppCategory = HandlersSupermarketAppCategory
export type AppTranslations = Record<string, HandlersSupermarketAppTranslation>

export const BOT_APPS_QUERY_KEY = 'bot-apps'
export const APP_CATEGORIES_QUERY_KEY = 'supermarket-categories'

/**
 * Query key of one bot+target App list. `invalidateBotApps`
 * invalidates by the two-element prefix so every target of a bot refreshes.
 */
export function botAppsQueryKey(botId: string, targetId: string): string[] {
  return [BOT_APPS_QUERY_KEY, botId, targetId]
}

// The Server resolves an empty target to the bot's current one; an explicit
// id is only sent when the caller picked one.
function workspaceTargetQuery(targetId: string): { workspace_target_id: string } | undefined {
  const trimmed = targetId.trim()
  return trimmed ? { workspace_target_id: trimmed } : undefined
}

export function useBotAppsQuery(botId: Ref<string>, targetId: Ref<string>, forceRefresh?: Ref<boolean>) {
  return useQuery({
    key: () => botAppsQueryKey(botId.value, targetId.value),
    query: async () => {
      const refresh = forceRefresh?.value ?? false
      if (forceRefresh) forceRefresh.value = false
      const { data } = await getBotsByBotIdApps({
        path: { bot_id: botId.value },
        query: { ...workspaceTargetQuery(targetId.value), refresh: refresh || undefined },
        throwOnError: true,
      })
      return data
    },
    enabled: () => !!botId.value,
  })
}

/** Refetches every target's App list of one bot. */
export function invalidateBotApps(
  queryCache: ReturnType<typeof useQueryCache>,
  botId: string,
): Promise<unknown> {
  return queryCache.invalidateQueries({ key: [BOT_APPS_QUERY_KEY, botId] })
}

/**
 * Compares every installed App with the registry and runs the dependency
 * update checks, returning the refreshed list.
 */
export async function checkAppUpdates(botId: string, targetId: string): Promise<AppListResponse> {
  const { data } = await postBotsByBotIdAppsCheckUpdates({
    path: { bot_id: botId },
    query: workspaceTargetQuery(targetId),
    throwOnError: true,
  })
  return data
}

export async function fetchAppRemovalPreview(botId: string, installationId: string): Promise<AppRemovalPreview> {
  const { data } = await getBotsByBotIdAppsByInstallationIdRemovalPreview({
    path: { bot_id: botId, installation_id: installationId },
    throwOnError: true,
  })
  return data
}

export async function beginAppConnectorOAuth(botId: string, installationId: string, connectorType: string, authMethod: string) {
  const { data } = await postBotsByBotIdAppsByInstallationIdConnectorsByConnectorTypeOauth({
    path: { bot_id: botId, installation_id: installationId, connector_type: connectorType },
    body: { auth_method: authMethod },
    throwOnError: true,
  })
  return data
}

export async function createAppConnectorCredential(
  botId: string,
  installationId: string,
  connectorType: string,
  authMethod: string,
  fields: Record<string, string>,
) {
  const { data } = await postBotsByBotIdAppsByInstallationIdConnectorsByConnectorTypeApiKey({
    path: { bot_id: botId, installation_id: installationId, connector_type: connectorType },
    body: { auth_method: authMethod, fields },
    throwOnError: true,
  })
  return data
}

/** The shared category table with localized names, across every enabled registry. */
export function useAppCategoriesQuery() {
  return useQuery({
    key: () => [APP_CATEGORIES_QUERY_KEY],
    query: async () => {
      const { data } = await getSupermarketCategories({ throwOnError: true })
      return data.data ?? []
    },
  })
}

type AppText = {
  name?: string
  description?: string
  app_id?: string
  translations?: AppTranslations
}

function localeKey(language: string): string {
  return language.toLowerCase().split(/[-_]/)[0] ?? 'en'
}

/** Localized App name: the translation for the UI language, else the manifest name. */
export function appDisplayName(item: AppText, language = 'en'): string {
  return item.translations?.[localeKey(language)]?.name?.trim()
    || item.name?.trim()
    || item.app_id?.trim()
    || ''
}

export function appDisplayDescription(item: AppText, language = 'en'): string {
  return item.translations?.[localeKey(language)]?.description?.trim()
    || item.description?.trim()
    || ''
}

/** Localized category name from the shared table, falling back to the App's recorded English name. */
export function categoryDisplayName(category: Pick<AppCategory, 'name' | 'names'> | undefined, fallback: string, language = 'en'): string {
  return category?.names?.[localeKey(language)]?.trim() || category?.name?.trim() || fallback
}

export function appInProgress(item: Pick<AppItem, 'status'>): boolean {
  return item.status === 'installing' || item.status === 'updating' || item.status === 'removing'
}

/** A newer release is known for this installation. */
export function appUpdateAvailable(item: Pick<AppItem, 'available_revision' | 'revision'>): boolean {
  const available = item.available_revision?.trim() ?? ''
  return !!available && available !== (item.revision ?? '')
}

/** Drives the tab count badge: Apps the user should act on. */
export function appNeedsAttention(item: AppItem): boolean {
  return item.status === 'partial' || item.status === 'failed' || appHasUpdates(item)
}

/** Dependencies of the App with a newer version known. */
export function appDependencyUpdates(item: Pick<AppItem, 'dependencies'>): AppDependencyItem[] {
  return (item.dependencies ?? []).filter(dep => !!dep.dependency && dependencyUpdateAvailable(dep.dependency))
}

/** Something on the App can be updated: its release or one of its dependencies. */
export function appHasUpdates(item: AppItem): boolean {
  return appUpdateAvailable(item) || appDependencyUpdates(item).length > 0
}

/** Stable identity of an App on a bot, independent of the installation record. */
export function appKey(item: Pick<AppItem, 'registry_id' | 'app_id'>): string {
  return `${item.registry_id ?? ''}/${item.app_id ?? ''}`
}
