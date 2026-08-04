import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { useLocalStorage } from '@vueuse/core'
import { fetchProjects, type BotProject } from '@/composables/api/useProjects'

// Client state around bot projects: the per-bot project list (shared by the
// sidebar grouping, the composer chip, and session creation) plus the per-bot
// "working project" — the project new sessions bind to by default. The
// working project is UI context, not business data, so it persists locally.
export const useProjectsStore = defineStore('projects', () => {
  const projectsByBot = ref<Record<string, BotProject[]>>({})
  const loadedBots = new Set<string>()
  const pendingLoads = new Map<string, Promise<void>>()

  const workingProjectByBot = useLocalStorage<Record<string, string>>(
    'workspace-working-project',
    {},
  )

  function projectsFor(botId: string | null | undefined): BotProject[] {
    const bid = (botId ?? '').trim()
    if (!bid) return []
    return projectsByBot.value[bid] ?? []
  }

  function projectById(botId: string | null | undefined, projectId: string | null | undefined): BotProject | null {
    const pid = (projectId ?? '').trim()
    if (!pid) return null
    return projectsFor(botId).find(project => project.id === pid) ?? null
  }

  // ensureProjects loads a bot's project list once; refreshProjects forces a
  // reload after a mutation. Failures leave the bot unloaded so the next
  // ensure retries instead of pinning an empty list.
  async function ensureProjects(botId: string | null | undefined): Promise<void> {
    const bid = (botId ?? '').trim()
    if (!bid || loadedBots.has(bid)) return
    const pending = pendingLoads.get(bid)
    if (pending) return pending
    const load = fetchProjects(bid)
      .then((projects) => {
        projectsByBot.value = { ...projectsByBot.value, [bid]: projects }
        loadedBots.add(bid)
      })
      .finally(() => {
        pendingLoads.delete(bid)
      })
    pendingLoads.set(bid, load)
    return load
  }

  async function refreshProjects(botId: string | null | undefined): Promise<void> {
    const bid = (botId ?? '').trim()
    if (!bid) return
    loadedBots.delete(bid)
    return ensureProjects(bid)
  }

  const workingProjectId = computed(() => (botId: string | null | undefined) => {
    const bid = (botId ?? '').trim()
    if (!bid) return ''
    return workingProjectByBot.value[bid] ?? ''
  })

  function workingProjectFor(botId: string | null | undefined): BotProject | null {
    const bid = (botId ?? '').trim()
    if (!bid) return null
    const pid = workingProjectByBot.value[bid] ?? ''
    if (!pid) return null
    const project = projectById(bid, pid)
    // A stale working project (archived, deleted, remote unbound) silently
    // degrades to "no project" once the list is loaded and disagrees.
    if (loadedBots.has(bid) && (!project || project.archived)) return null
    return project
  }

  function setWorkingProject(botId: string | null | undefined, projectId: string | null) {
    const bid = (botId ?? '').trim()
    if (!bid) return
    const next = { ...workingProjectByBot.value }
    const pid = (projectId ?? '').trim()
    if (pid) next[bid] = pid
    else delete next[bid]
    workingProjectByBot.value = next
  }

  // sessionProjectIdFor answers "which project should this new session bind
  // to". ACP sessions can only run in native-workspace projects (the runtime
  // cannot reach a remote computer yet), so a remote working project is
  // skipped rather than producing a session the backend would reject.
  function sessionProjectIdFor(botId: string | null | undefined, opts: { acp?: boolean } = {}): string {
    const project = workingProjectFor(botId)
    if (!project?.id) return ''
    if (opts.acp && project.target_kind === 'remote') return ''
    return project.id
  }

  return {
    projectsByBot,
    projectsFor,
    projectById,
    ensureProjects,
    refreshProjects,
    workingProjectId,
    workingProjectFor,
    setWorkingProject,
    sessionProjectIdFor,
  }
})
