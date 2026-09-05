import { computed, ref, shallowRef, watch, watchEffect } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useActiveGate } from './useActiveGate'
import { useChatViewTarget } from './useChatViewContext'
import { useContextLifecycle } from './useContextLifecycle'
import { useSessionCompactions } from './useSessionCompactions'
import {
  buildRowMap,
  createTrajectoryRowBuilder,
  foldTrajectoryStats,
  lifecycleByTurnId,
  previousLifecycleByRun,
  type TrajectoryRow,
  type TrajectoryStats,
} from './trajectory-model'
import { rowMapGeometry, type TimelineMode } from './trajectory-view'

// The trajectory reads the session's shared transcript window: the same
// history pages and live projection the chat panel already holds, so opening
// it moves no second copy of the conversation over the wire.
export function useTrajectory() {
  const { t } = useI18n()
  const chatStore = useChatStore()
  const target = useChatViewTarget()
  const lifecycle = useContextLifecycle()
  const compactionPages = useSessionCompactions()
  const compactions = compactionPages.compactions

  const transcript = computed(() => {
    const { botId, sessionId, viewId } = target.value
    return botId && sessionId ? chatStore.chatView({ botId, sessionId, viewId }).transcript : null
  })
  const messages = computed(() => transcript.value?.visibleMessages.value ?? [])
  const loadingMessages = computed(() => transcript.value?.loadingMessages.value ?? false)
  const lifecycleByTurn = computed(() => lifecycleByTurnId(lifecycle.turns.value))
  const previousByRun = computed(() => previousLifecycleByRun(lifecycle.turns.value, lifecycle.hasOlder.value))
  const buildRows = createTrajectoryRowBuilder()
  // A hidden trajectory tab keeps its last rows and stats instead of
  // rebuilding them on every streamed token; it catches up when shown.
  const active = useActiveGate()
  const rows = shallowRef<TrajectoryRow[]>([])
  const stats = shallowRef<TrajectoryStats>(foldTrajectoryStats([], new Map()))
  watchEffect(() => {
    if (!active.value) return
    rows.value = buildRows(messages.value, lifecycleByTurn.value, previousByRun.value, compactions.value)
  })
  watchEffect(() => {
    if (!active.value) return
    stats.value = foldTrajectoryStats(messages.value, lifecycleByTurn.value)
  })

  const selectedKey = ref<string | null>(null)
  const selectedRow = computed(() => rows.value.find(row => row.key === selectedKey.value) ?? null)
  watch(() => target.value.sessionId, () => {
    selectedKey.value = null
  })

  const mode = ref<TimelineMode>('duration')
  const segments = computed(() => buildRowMap(rows.value))
  const bars = computed(() => rowMapGeometry(segments.value, mode.value))

  const hasOlder = computed(() => (transcript.value?.hasMoreOlder.value ?? false) || lifecycle.canLoadOlder.value || compactionPages.canLoadOlder.value)
  const loadingOlder = computed(() => (transcript.value?.loadingOlder.value ?? false) || lifecycle.loadingOlder.value || compactionPages.loadingOlder.value)

  async function loadOlder() {
    const tasks: Promise<unknown>[] = []
    if (transcript.value?.hasMoreOlder.value) tasks.push(chatStore.loadOlderMessages(target.value))
    if (lifecycle.canLoadOlder.value) tasks.push(lifecycle.loadOlder())
    if (compactionPages.canLoadOlder.value) tasks.push(compactionPages.loadOlder())
    try {
      await Promise.all(tasks)
    } catch (error) {
      toast.error(resolveApiErrorMessage(error, t('chat.lifecycle.loadFailed')))
    }
  }

  function select(key: string | null) {
    selectedKey.value = selectedKey.value === key ? null : key
  }

  // The strip focuses without toggling: clicking the selected bar again keeps
  // the inspector open on it.
  function focus(key: string) {
    selectedKey.value = key
  }

  return {
    hasTarget: computed(() => !!target.value.sessionId),
    rows,
    stats,
    fragmentPreviews: lifecycle.fragmentPreviews,
    loadingMessages,
    selectedKey,
    selectedRow,
    bars,
    mode,
    hasOlder,
    loadingOlder,
    loadOlder,
    select,
    focus,
  }
}
