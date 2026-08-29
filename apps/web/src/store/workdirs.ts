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

  // Before the list loads, retain the persisted ID conservatively. A raw
  // Folder selection must suppress ACP prewarm instead of briefly looking like
  // "no Folder" and starting a runtime against Primary. Once loaded, validate
  // the ID so an archived/deleted Folder degrades to no binding.
  function sessionWorkdirIdFor(botId: string | null | undefined): string {
    const bid = (botId ?? '').trim()
    if (!bid) return ''
    const workdirId = workingWorkdirByBot.value[bid]?.trim() ?? ''
    if (!workdirId || !loadedBots.has(bid)) return workdirId
    const workdir = workdirById(bid, workdirId)
    return workdir && !workdir.archived ? workdirId : ''
  }

  // The draft Folder binding with its target kind and path. Kind stays empty
  // until the authoritative list confirms it, so callers treating only an
  // explicit "native" as safe-to-prewarm degrade conservatively while the
  // list loads or when the Folder is unknown.
  function sessionWorkdirBindingFor(
    botId: string | null | undefined,
  ): { id: string, kind: string, path: string } {
    const bid = (botId ?? '').trim()
    const id = sessionWorkdirIdFor(bid)
    if (!id || !loadedBots.has(bid)) return { id, kind: '', path: '' }
    const workdir = workdirById(bid, id)
    return {
      id,
      kind: (workdir?.target_kind ?? '').trim(),
      path: (workdir?.path ?? '').trim(),
    }
  }

  // Session creation waits for the authoritative list before committing its
  // immutable Folder binding. A load failure rejects creation rather than
  // silently creating an unbound Session on Primary.
  async function resolveSessionWorkdirIdFor(botId: string | null | undefined): Promise<string> {
    const bid = (botId ?? '').trim()
    if (!bid || !sessionWorkdirIdFor(bid)) return ''
    await ensureWorkdirs(bid)
    return sessionWorkdirIdFor(bid)
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
    sessionWorkdirBindingFor,
    resolveSessionWorkdirIdFor,
  }
})
