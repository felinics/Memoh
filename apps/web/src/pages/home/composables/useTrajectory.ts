import { computed, ref, shallowRef, watch, watchEffect } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useActiveGate } from './useActiveGate'
import { useChatViewTarget } from './useChatViewContext'
import { useContextLifecycle } from './useContextLifecycle'
import { useSessionCompactions } from './useSessionCompactions'
import { useContextTrajectory } from './useContextTrajectory'
import { mergeTrajectoryCaptures } from './context-trajectory-view'
import type { ContextTrajectoryFilter } from './context-trajectory.types'
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
  const active = useActiveGate()
  const lifecycle = useContextLifecycle()
  const captures = useContextTrajectory(active)
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
  const rows = shallowRef<TrajectoryRow[]>([])
  const filter = shallowRef<ContextTrajectoryFilter>('all')
  const visibleRows = computed(() => rows.value.filter(row => filter.value === 'all'
    || (filter.value === 'requests' ? row.kind === 'request' : row.kind === 'context' || row.kind === 'system' || row.kind === 'request')))
  const stats = shallowRef<TrajectoryStats>(foldTrajectoryStats([], new Map()))
  watchEffect(() => {
    if (!active.value) return
    rows.value = mergeTrajectoryCaptures(buildRows(messages.value, lifecycleByTurn.value, previousByRun.value, compactions.value), captures.events.value, lifecycle.turns.value)
  })
  watchEffect(() => {
    if (!active.value) return
    stats.value = foldTrajectoryStats(messages.value, lifecycleByTurn.value)
  })

  const selectedKey = ref<string | null>(null)
  const selectedRow = computed(() => visibleRows.value.find(row => row.key === selectedKey.value) ?? null)
  watch(() => target.value.sessionId, () => {
    selectedKey.value = null
  })
  watch(filter, () => { selectedKey.value = null })

  const mode = ref<TimelineMode>('duration')
  const segments = computed(() => buildRowMap(visibleRows.value))
  const bars = computed(() => rowMapGeometry(segments.value, mode.value))

  const hasOlder = computed(() => (transcript.value?.hasMoreOlder.value ?? false) || lifecycle.canLoadOlder.value || compactionPages.canLoadOlder.value || captures.canLoadOlder.value)
  const loadingOlder = computed(() => (transcript.value?.loadingOlder.value ?? false) || lifecycle.loadingOlder.value || compactionPages.loadingOlder.value || captures.loadingOlder.value)

  async function loadOlder() {
    const tasks: Promise<unknown>[] = []
    if (transcript.value?.hasMoreOlder.value) tasks.push(chatStore.loadOlderMessages(target.value))
    if (lifecycle.canLoadOlder.value) tasks.push(lifecycle.loadOlder())
    if (compactionPages.canLoadOlder.value) tasks.push(compactionPages.loadOlder())
    if (captures.canLoadOlder.value) tasks.push(captures.loadOlder())
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

  async function refreshContext() {
    await Promise.all([captures.refresh(), lifecycle.refresh()])
  }

  return {
    hasTarget: computed(() => !!target.value.sessionId),
    rows: visibleRows,
    filter,
    captureCount: computed(() => captures.events.value.length),
    loadingContext: computed(() => captures.status.value === 'pending' || lifecycle.status.value === 'pending'),
    hasLoadError: computed(() => !!captures.error.value || lifecycle.status.value === 'error' || compactionPages.status.value === 'error'),
    hasCaptureGap: captures.hasGap,
    hasCaptureErrors: computed(() => captures.invalidEntries.value > 0 || captures.events.value.some(event => (event.capture_errors ?? 0) > 0)
      || lifecycle.turns.value.some(turn => (turn.snapshot?.trajectory?.errors ?? 0) > 0 || (turn.snapshot?.trajectory?.pending ?? 0) > 0)),
    refreshContext,
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
