import type { Ref } from 'vue'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  getBotsByBotIdDependencies,
  getBotsByBotIdDependenciesByDepIdScript,
  postBotsByBotIdDependenciesByDepIdRollback,
  postBotsByBotIdDependenciesCheckUpdates,
  postBotsByBotIdDependenciesPreflight,
  type HandlersWorkspaceDependencyItem,
  type HandlersWorkspaceDependencyListResponse,
  type HandlersWorkspaceDependencyOperationResponse,
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
export type DependencyOperationResponse = HandlersWorkspaceDependencyOperationResponse
/** The operations that stream a log (design §11). Rollback is synchronous. */
export type DependencyOperationAction = 'install' | 'update' | 'reinstall' | 'remove'

export const BOT_DEPENDENCIES_QUERY_KEY = 'bot-dependencies'

/**
 * Query key of one bot+target dependency list. `invalidateBotDependencies`
 * invalidates by the two-element prefix so every target of a bot refreshes.
 */
export function botDependenciesQueryKey(botId: string, targetId: string): string[] {
  return [BOT_DEPENDENCIES_QUERY_KEY, botId, targetId]
}

// The Server resolves an empty target to the bot's current one; an explicit
// id is only sent when the caller picked a target, so the primary target and
// "no selection" share the Server's default path.
function workspaceTargetQuery(targetId: string): { workspace_target_id: string } | undefined {
  const trimmed = targetId.trim()
  return trimmed ? { workspace_target_id: trimmed } : undefined
}

export function useBotDependenciesQuery(botId: Ref<string>, targetId: Ref<string>) {
  return useQuery({
    key: () => botDependenciesQueryKey(botId.value, targetId.value),
    query: async () => {
      const { data } = await getBotsByBotIdDependencies({
        path: { bot_id: botId.value },
        query: workspaceTargetQuery(targetId.value),
        throwOnError: true,
      })
      return data
    },
    enabled: () => !!botId.value,
  })
}

/**
 * Blocking readiness check before an agent is enabled (design §9.3). Never
 * starts the workspace: when it is not running `items` is empty and
 * `workspace_state` says why.
 */
export async function preflightDependencies(
  botId: string,
  targetId: string,
  dependencyIds: string[],
): Promise<PreflightResponse> {
  const { data } = await postBotsByBotIdDependenciesPreflight({
    path: { bot_id: botId },
    body: {
      dependency_ids: dependencyIds,
      workspace_target_id: workspaceTargetQuery(targetId)?.workspace_target_id,
    },
    throwOnError: true,
  })
  return data
}

/** Switches back to the previously kept version. Pure data; nothing streams. */
export async function rollbackDependency(
  botId: string,
  targetId: string,
  depId: string,
): Promise<DependencyOperationResponse> {
  const { data } = await postBotsByBotIdDependenciesByDepIdRollback({
    path: { bot_id: botId, dep_id: depId },
    query: workspaceTargetQuery(targetId),
    throwOnError: true,
  })
  return data
}

/**
 * Manual refresh (design §10.3): re-discovers the workspace and runs the
 * upstream check of every installed tool dependency, returning the refreshed
 * list. Callers still invalidate the query so the panel re-renders from cache.
 */
export async function checkDependencyUpdates(
  botId: string,
  targetId: string,
): Promise<DependencyListResponse> {
  const { data } = await postBotsByBotIdDependenciesCheckUpdates({
    path: { bot_id: botId },
    query: workspaceTargetQuery(targetId),
    throwOnError: true,
  })
  return data
}

/**
 * The exact script text a dependency action would feed the workspace shell,
 * prelude included (WD-API-001). Scripts never touch the workspace disk, so
 * this is the only way to inspect them.
 */
export async function fetchDependencyScript(
  botId: string,
  targetId: string,
  depId: string,
  action: ScriptAction,
): Promise<ScriptResponse> {
  const { data } = await getBotsByBotIdDependenciesByDepIdScript({
    path: { bot_id: botId, dep_id: depId },
    query: { action, ...workspaceTargetQuery(targetId) },
    throwOnError: true,
  })
  return data
}

/** Refetches every target's dependency list of one bot. */
export function invalidateBotDependencies(
  queryCache: ReturnType<typeof useQueryCache>,
  botId: string,
): Promise<unknown> {
  return queryCache.invalidateQueries({ key: [BOT_DEPENDENCIES_QUERY_KEY, botId] })
}
