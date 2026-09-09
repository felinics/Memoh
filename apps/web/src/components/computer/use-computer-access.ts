import { computed } from 'vue'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import {
  deleteBotsByBotIdWorkspaceTargetsByTargetId,
  getUsersMeComputerAccess,
  getUsersMeRuntimes,
  putBotsByBotIdWorkspaceTargetsRemotesByRuntimeId,
  type WorkspaceWorkspaceTargetGrant,
} from '@memohai/sdk'

// Account-level Computer ACL: the grants query is the single read model behind
// the /runtimes badge, the access dialog (both directions), and the bot
// Computer page merges. Writes stay on the bot-scoped mount/unmount endpoints;
// every write invalidates this key plus the per-bot workspace-targets list.
// The key head is excluded from the persisted query cache (see
// lib/query-cache-persistence.ts): ACL state must always be server-fresh.

export const COMPUTER_ACCESS_GRANTS_KEY = ['computer-access-grants'] as const

export type ComputerAccessGrant = WorkspaceWorkspaceTargetGrant & {
  target_id: string
  bot_id: string
  runtime_id: string
}

export function useComputerAccessGrants() {
  const { data, ...query } = useQuery({
    key: [...COMPUTER_ACCESS_GRANTS_KEY],
    query: async () => {
      const { data } = await getUsersMeComputerAccess({ throwOnError: true })
      return data
    },
    refetchOnWindowFocus: true,
  })
  const grants = computed<ComputerAccessGrant[]>(() => (
    (data.value?.grants ?? []).filter((grant): grant is ComputerAccessGrant => (
      typeof grant.target_id === 'string' && grant.target_id.length > 0
      && typeof grant.bot_id === 'string' && grant.bot_id.length > 0
      && typeof grant.runtime_id === 'string' && grant.runtime_id.length > 0
    ))
  ))
  return { grants, ...query }
}

export function useAccountRuntimes() {
  const { data, ...query } = useQuery({
    key: ['remote-runtimes'],
    query: async () => {
      const { data } = await getUsersMeRuntimes({ throwOnError: true })
      return data
    },
    refetchOnWindowFocus: true,
  })
  return { runtimes: data, ...query }
}

export function useComputerAccessActions() {
  const queryCache = useQueryCache()

  function invalidate(botId: string) {
    void queryCache.invalidateQueries({ key: [...COMPUTER_ACCESS_GRANTS_KEY] })
    void queryCache.invalidateQueries({ key: ['bot-workspace-targets', botId] })
  }

  const { mutateAsync: grantAccess } = useMutation({
    mutation: async ({ botId, runtimeId }: { botId: string, runtimeId: string }) => {
      const { data } = await putBotsByBotIdWorkspaceTargetsRemotesByRuntimeId({
        path: { bot_id: botId, runtime_id: runtimeId },
        throwOnError: true,
      })
      return data
    },
    onSettled: (_data, _error, vars) => invalidate(vars.botId),
  })

  const { mutateAsync: revokeAccess } = useMutation({
    mutation: async ({ botId, targetId }: { botId: string, targetId: string }) => {
      await deleteBotsByBotIdWorkspaceTargetsByTargetId({
        path: { bot_id: botId, target_id: targetId },
        throwOnError: true,
      })
    },
    onSettled: (_data, _error, vars) => invalidate(vars.botId),
  })

  return { grantAccess, revokeAccess }
}
