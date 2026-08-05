import {
  deleteBotsByBotIdWorkdirsByWorkdirId,
  getBotsByBotIdWorkdirs,
  patchBotsByBotIdWorkdirsByWorkdirId,
  postBotsByBotIdWorkdirs,
  type WorkdirWorkdir,
} from '@memohai/sdk'

// A bot workdir: a named (workspace target, directory) pair. Sessions bind to
// one immutably at creation; the backend derives their working directory from
// it for their whole life.
export type BotWorkdir = WorkdirWorkdir

export async function fetchWorkdirs(botId: string, includeArchived = false): Promise<BotWorkdir[]> {
  const { data } = await getBotsByBotIdWorkdirs({
    path: { bot_id: botId },
    query: includeArchived ? { include_archived: true } : undefined,
    throwOnError: true,
  })
  return data.workdirs ?? []
}

export async function createWorkdir(
  botId: string,
  input: { name: string, path: string, workspaceTargetId?: string },
): Promise<BotWorkdir> {
  const { data } = await postBotsByBotIdWorkdirs({
    path: { bot_id: botId },
    body: {
      name: input.name,
      path: input.path,
      workspace_target_id: input.workspaceTargetId?.trim() || undefined,
    },
    throwOnError: true,
  })
  return data
}

export async function renameWorkdir(botId: string, workdirId: string, name: string): Promise<BotWorkdir> {
  const { data } = await patchBotsByBotIdWorkdirsByWorkdirId({
    path: { bot_id: botId, workdir_id: workdirId },
    body: { name },
    throwOnError: true,
  })
  return data
}

export async function archiveWorkdir(botId: string, workdirId: string): Promise<void> {
  await deleteBotsByBotIdWorkdirsByWorkdirId({
    path: { bot_id: botId, workdir_id: workdirId },
    throwOnError: true,
  })
}
