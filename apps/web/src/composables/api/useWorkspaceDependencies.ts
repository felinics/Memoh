import type { Ref } from 'vue'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  getBotsByBotIdDependencies,
  getBotsByBotIdDependenciesByDepIdScript,
  postBotsByBotIdDependenciesByDepIdPrepare,
  postBotsByBotIdDependenciesByDepIdRepairPrepare,
  postBotsByBotIdDependenciesByDepIdRepairRetry,
  postBotsByBotIdDependenciesCheckUpdates,
  postBotsByBotIdDependenciesPreflight,
  type HandlersWorkspaceDependencyItem,
  type HandlersWorkspaceDependencyListResponse,
  type HandlersWorkspaceDependencyPlatform,
  type HandlersWorkspaceDependencyPreflightItem,
  type HandlersWorkspaceDependencyPreflightResponse,
  type HandlersWorkspaceDependencyScriptEnv,
  type HandlersWorkspaceDependencyScriptResponse,
} from '@memohai/sdk'

// Domain aliases over the generated SDK types. The generated names carry the
// Go handler prefix; the panel, its dialogs, and the enable flow speak in
// dependency terms, and pointing every consumer at one alias keeps a future
// swagger rename to a single edit.
export type DependencyItem = HandlersWorkspaceDependencyItem
export type DependencyListResponse = HandlersWorkspaceDependencyListResponse
export type DependencyPlatform = HandlersWorkspaceDependencyPlatform
export type DependencyWorkspaceState = NonNullable<DependencyListResponse['workspace_state']>
export type DependencyCategory = NonNullable<DependencyItem['category']>
export type DependencySource = NonNullable<DependencyItem['source']>
export type DependencyStatus = NonNullable<DependencyItem['status']>
/** What the Server says may be requested right now (`items[].actions`). */
export type DependencyAvailableAction = NonNullable<DependencyItem['actions']>[number]
export type PreflightResponse = HandlersWorkspaceDependencyPreflightResponse
export type PreflightItem = HandlersWorkspaceDependencyPreflightItem
export type PreflightState = NonNullable<PreflightItem['state']>
export type ScriptResponse = HandlersWorkspaceDependencyScriptResponse
export type ScriptEnv = HandlersWorkspaceDependencyScriptEnv
export type ScriptAction = NonNullable<ScriptResponse['action']>
/**
 * The dependency operations that stream a log. Install here means a retry or
 * an overlay for a dependency an App already references; removal belongs
 * to the App.
 */
export type DependencyOperationAction = 'install' | 'update' | 'reinstall'

export type DependencyPreparedInstallation = Awaited<ReturnType<typeof prepareDependencyInstallation>>

/** Explicit Manage action: resolve an exact release before the user confirms it. */
export async function prepareDependencyInstallation(
  botId: string,
  depId: string,
  action: DependencyOperationAction,
  version: string,
  definitionRevision: string,
) {
  const { data } = await postBotsByBotIdDependenciesByDepIdPrepare({
    path: { bot_id: botId, dep_id: depId },
    body: { action, version: version || undefined, definition_revision: definitionRevision },
    throwOnError: true,
  })
  return data
}

export async function prepareDependencyRepair(botId: string, depId: string, version: string, definitionRevision: string) {
  const { data } = await postBotsByBotIdDependenciesByDepIdRepairPrepare({
    path: { bot_id: botId, dep_id: depId },
    body: { version: version || undefined, definition_revision: definitionRevision || undefined },
    throwOnError: true,
  })
  return data
}

export async function retryDependencyRepair(botId: string, depId: string) {
  const { data } = await postBotsByBotIdDependenciesByDepIdRepairRetry({
    path: { bot_id: botId, dep_id: depId },
    throwOnError: true,
  })
  return data
}

export const BOT_DEPENDENCIES_QUERY_KEY = 'bot-dependencies'

/**
 * Query key of one bot dependency list. `invalidateBotDependencies`
 * invalidates by the two-element prefix to refresh the bot.
 */
export function botDependenciesQueryKey(botId: string): string[] {
  return [BOT_DEPENDENCIES_QUERY_KEY, botId]
}

export function useBotDependenciesQuery(botId: Ref<string>, forceRefresh?: Ref<boolean>) {
  return useQuery({
    key: () => botDependenciesQueryKey(botId.value),
    query: async () => {
      const refresh = forceRefresh?.value ?? false
      if (forceRefresh) forceRefresh.value = false
      const { data } = await getBotsByBotIdDependencies({
        path: { bot_id: botId.value },
        query: { refresh: refresh || undefined },
        throwOnError: true,
      })
      return data
    },
    enabled: () => !!botId.value,
  })
}

/**
 * Blocking readiness check before an agent is enabled. Never
 * starts the workspace: when it is not running `items` is empty and
 * `workspace_state` says why.
 */
export async function preflightDependencies(
  botId: string,
  dependencyIds: string[],
): Promise<PreflightResponse> {
  const { data } = await postBotsByBotIdDependenciesPreflight({
    path: { bot_id: botId },
    body: {
      dependency_ids: dependencyIds,
    },
    throwOnError: true,
  })
  return data
}

/**
 * Manual refresh re-discovers the workspace and runs the
 * upstream check of every installed tool dependency, returning the refreshed
 * list. Callers still invalidate the query so the panel re-renders from cache.
 */
export async function checkDependencyUpdates(
  botId: string,
): Promise<DependencyListResponse> {
  const { data } = await postBotsByBotIdDependenciesCheckUpdates({
    path: { bot_id: botId },
    throwOnError: true,
  })
  return data
}

/**
 * The exact script text a dependency action would feed the workspace shell,
 * prelude included. Scripts never touch the workspace disk, so
 * this is the only way to inspect them.
 */
export async function fetchDependencyScript(
  botId: string,
  depId: string,
  action: ScriptAction,
  definitionRevision?: string,
): Promise<ScriptResponse> {
  const { data } = await getBotsByBotIdDependenciesByDepIdScript({
    path: { bot_id: botId, dep_id: depId },
    query: { action, definition_revision: definitionRevision || undefined },
    throwOnError: true,
  })
  return data
}

/** Refetches the bot's dependency list. */
export function invalidateBotDependencies(
  queryCache: ReturnType<typeof useQueryCache>,
  botId: string,
): Promise<unknown> {
  return queryCache.invalidateQueries({ key: [BOT_DEPENDENCIES_QUERY_KEY, botId] })
}
