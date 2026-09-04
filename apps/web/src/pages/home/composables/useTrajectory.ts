import { computed, ref, watch } from 'vue'
import { useChatStore } from '@/store/chat-list'
import type { ChatAssistantTurn } from '@/store/chat/types'
import { useChatViewTarget } from './useChatViewContext'
import { useContextLifecycle } from './useContextLifecycle'
import {
  buildTrajectoryRows,
  buildTurnTimeline,
  foldTrajectoryStats,
  lifecycleByTurnId,
} from './trajectory-model'
import type { TimelineMode } from './trajectory-view'

// The trajectory reads the session's shared transcript window: the same
// history pages and live projection the chat panel already holds, so opening
// it moves no second copy of the conversation over the wire.
export function useTrajectory() {
  const chatStore = useChatStore()
  const target = useChatViewTarget()
  const lifecycle = useContextLifecycle()

  const transcript = computed(() => {
    const { botId, sessionId, viewId } = target.value
    return botId && sessionId ? chatStore.chatView({ botId, sessionId, viewId }).transcript : null
  })
  const messages = computed(() => transcript.value?.visibleMessages.value ?? [])
  const lifecycleByTurn = computed(() => lifecycleByTurnId(lifecycle.turns.value))
  const rows = computed(() => buildTrajectoryRows(messages.value, lifecycleByTurn.value))
  const stats = computed(() => foldTrajectoryStats(messages.value, lifecycleByTurn.value))

  const selectedKey = ref<string | null>(null)
  const selectedRow = computed(() => rows.value.find(row => row.key === selectedKey.value) ?? null)
  watch(() => target.value.sessionId, () => {
    selectedKey.value = null
  })

  const latestAssistantTurn = computed<ChatAssistantTurn | null>(() => {
    for (let index = messages.value.length - 1; index >= 0; index -= 1) {
      const turn = messages.value[index]!
      if (turn.role === 'assistant') return turn
    }
    return null
  })
  const focusedTurn = computed<ChatAssistantTurn | null>(() => {
    const turnId = selectedRow.value?.turnId
    if (turnId) {
      const turn = messages.value.find(message => message.role === 'assistant' && message.turnId === turnId)
      if (turn && turn.role === 'assistant') return turn
    }
    return latestAssistantTurn.value
  })
  const timeline = computed(() => (focusedTurn.value ? buildTurnTimeline(focusedTurn.value) : null))
  const mode = ref<TimelineMode>('duration')

  const hasOlder = computed(() => (transcript.value?.hasMoreOlder.value ?? false) || lifecycle.canLoadOlder.value)
  const loadingOlder = computed(() => (transcript.value?.loadingOlder.value ?? false) || lifecycle.loadingOlder.value)

  async function loadOlder() {
    const tasks: Promise<unknown>[] = []
    if (transcript.value?.hasMoreOlder.value) tasks.push(chatStore.loadOlderMessages(target.value))
    if (lifecycle.canLoadOlder.value) tasks.push(lifecycle.loadOlder())
    await Promise.all(tasks)
  }

  function select(key: string | null) {
    selectedKey.value = selectedKey.value === key ? null : key
  }

  return {
    hasTarget: computed(() => !!target.value.sessionId),
    rows,
    stats,
    selectedKey,
    selectedRow,
    focusedTurn,
    timeline,
    mode,
    hasOlder,
    loadingOlder,
    loadOlder,
    select,
    lifecycleStatus: lifecycle.status,
  }
}
