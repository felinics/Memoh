import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useChatViewTarget } from './useChatViewContext'
import { useContextLifecycle } from './useContextLifecycle'
import {
  buildRowMap,
  createTrajectoryRowBuilder,
  foldTrajectoryStats,
  lifecycleByTurnId,
  previousLifecycleByRun,
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

  const transcript = computed(() => {
    const { botId, sessionId, viewId } = target.value
    return botId && sessionId ? chatStore.chatView({ botId, sessionId, viewId }).transcript : null
  })
  const messages = computed(() => transcript.value?.visibleMessages.value ?? [])
  const loadingMessages = computed(() => transcript.value?.loadingMessages.value ?? false)
  const lifecycleByTurn = computed(() => lifecycleByTurnId(lifecycle.turns.value))
  const previousByRun = computed(() => previousLifecycleByRun(lifecycle.turns.value, lifecycle.hasOlder.value))
  const buildRows = createTrajectoryRowBuilder()
  const rows = computed(() => buildRows(messages.value, lifecycleByTurn.value, previousByRun.value))
  const stats = computed(() => foldTrajectoryStats(messages.value, lifecycleByTurn.value))

  const selectedKey = ref<string | null>(null)
  const selectedRow = computed(() => rows.value.find(row => row.key === selectedKey.value) ?? null)
  watch(() => target.value.sessionId, () => {
    selectedKey.value = null
  })

  const mode = ref<TimelineMode>('duration')
  const segments = computed(() => buildRowMap(rows.value))
  const bars = computed(() => rowMapGeometry(segments.value, mode.value))

  const hasOlder = computed(() => (transcript.value?.hasMoreOlder.value ?? false) || lifecycle.canLoadOlder.value)
  const loadingOlder = computed(() => (transcript.value?.loadingOlder.value ?? false) || lifecycle.loadingOlder.value)

  async function loadOlder() {
    const tasks: Promise<unknown>[] = []
    if (transcript.value?.hasMoreOlder.value) tasks.push(chatStore.loadOlderMessages(target.value))
    if (lifecycle.canLoadOlder.value) tasks.push(lifecycle.loadOlder())
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
