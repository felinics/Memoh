import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { useLocalStorage } from '@vueuse/core'
import { fetchWorkdirs, type BotWorkdir } from '@/composables/api/useWorkdirs'

// Client state around bot workdirs: the per-bot workdir list (shared by the
// sidebar grouping, the composer chip, and session creation) plus the per-bot
// "working workdir" — the workdir new sessions bind to by default. The
// working workdir is UI context, not business data, so it persists locally.
export const useWorkdirsStore = defineStore('workdirs', () => {
  const workdirsByBot = ref<Record<string, BotWorkdir[]>>({})
  const loadedBots = new Set<string>()
  const pendingLoads = new Map<string, Promise<void>>()

  const workingWorkdirByBot = useLocalStorage<Record<string, string>>(
    'workspace-working-workdir',
    {},
  )

  function workdirsFor(botId: string | null | undefined): BotWorkdir[] {
    const bid = (botId ?? '').trim()
    if (!bid) return []
    return workdirsByBot.value[bid] ?? []
  }

  function workdirById(botId: string | null | undefined, workdirId: string | null | undefined): BotWorkdir | null {
    const pid = (workdirId ?? '').trim()
    if (!pid) return null
    return workdirsFor(botId).find(workdir => workdir.id === pid) ?? null
  }

  // ensureWorkdirs loads a bot's workdir list once; refreshWorkdirs forces a
  // reload after a mutation. Failures leave the bot unloaded so the next
  // ensure retries instead of pinning an empty list.
  async function ensureWorkdirs(botId: string | null | undefined): Promise<void> {
    const bid = (botId ?? '').trim()
    if (!bid || loadedBots.has(bid)) return
    const pending = pendingLoads.get(bid)
    if (pending) return pending
    const load = fetchWorkdirs(bid)
      .then((workdirs) => {
        workdirsByBot.value = { ...workdirsByBot.value, [bid]: workdirs }
        loadedBots.add(bid)
      })
      .finally(() => {
        pendingLoads.delete(bid)
      })
    pendingLoads.set(bid, load)
    return load
  }

  async function refreshWorkdirs(botId: string | null | undefined): Promise<void> {
    const bid = (botId ?? '').trim()
    if (!bid) return
    loadedBots.delete(bid)
    return ensureWorkdirs(bid)
  }

  const workingWorkdirId = computed(() => (botId: string | null | undefined) => {
    const bid = (botId ?? '').trim()
    if (!bid) return ''
    return workingWorkdirByBot.value[bid] ?? ''
  })

  function workingWorkdirFor(botId: string | null | undefined): BotWorkdir | null {
    const bid = (botId ?? '').trim()
    if (!bid) return null
    const pid = workingWorkdirByBot.value[bid] ?? ''
    if (!pid) return null
    const workdir = workdirById(bid, pid)
    // A stale working workdir (archived, deleted, remote unbound) silently
    // degrades to "no workdir" once the list is loaded and disagrees.
    if (loadedBots.has(bid) && (!workdir || workdir.archived)) return null
    return workdir
  }

  function setWorkingWorkdir(botId: string | null | undefined, workdirId: string | null) {
    const bid = (botId ?? '').trim()
    if (!bid) return
    const next = { ...workingWorkdirByBot.value }
    const pid = (workdirId ?? '').trim()
    if (pid) next[bid] = pid
    else delete next[bid]
    workingWorkdirByBot.value = next
  }

  // sessionWorkdirIdFor answers "which workdir should this new session bind
  // to". External Agent sessions can only run in native-workspace workdirs (the runtime
  // cannot reach a remote computer yet), so a remote working workdir is
  // skipped rather than producing a session the backend would reject.
  function sessionWorkdirIdFor(botId: string | null | undefined, opts: { externalAgent?: boolean } = {}): string {
    const workdir = workingWorkdirFor(botId)
    if (!workdir?.id) return ''
    if (opts.externalAgent && workdir.target_kind === 'remote') return ''
    return workdir.id
  }

  return {
    workdirsByBot,
    workdirsFor,
    workdirById,
    ensureWorkdirs,
    refreshWorkdirs,
    workingWorkdirId,
    workingWorkdirFor,
    setWorkingWorkdir,
    sessionWorkdirIdFor,
  }
})
