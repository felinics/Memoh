import {
  deleteBotsByBotIdProjectsByProjectId,
  getBotsByBotIdProjects,
  patchBotsByBotIdProjectsByProjectId,
  postBotsByBotIdProjects,
  type ProjectProject,
} from '@memohai/sdk'

// A bot project: a named (workspace target, directory) pair. Sessions bind to
// one immutably at creation; the backend derives their working directory from
// it for their whole life.
export type BotProject = ProjectProject

export async function fetchProjects(botId: string, includeArchived = false): Promise<BotProject[]> {
  const { data } = await getBotsByBotIdProjects({
    path: { bot_id: botId },
    query: includeArchived ? { include_archived: true } : undefined,
    throwOnError: true,
  })
  return data.projects ?? []
}

export async function createProject(
  botId: string,
  input: { name: string, path: string, workspaceTargetId?: string },
): Promise<BotProject> {
  const { data } = await postBotsByBotIdProjects({
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

export async function renameProject(botId: string, projectId: string, name: string): Promise<BotProject> {
  const { data } = await patchBotsByBotIdProjectsByProjectId({
    path: { bot_id: botId, project_id: projectId },
    body: { name },
    throwOnError: true,
  })
  return data
}

export async function archiveProject(botId: string, projectId: string): Promise<void> {
  await deleteBotsByBotIdProjectsByProjectId({
    path: { bot_id: botId, project_id: projectId },
    throwOnError: true,
  })
}
