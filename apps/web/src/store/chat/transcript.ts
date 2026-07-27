import { reactive, ref, type Ref } from 'vue'
import type {
  ChatAttachment,
  FetchMessagesOptions,
  UIMessage,
  UITurn,
} from '@/composables/api/useChat.types'
import {
  mergeApprovalState,
  nextId,
  serverMessageId,
} from '../chat-list.normalize'
import { upsertById } from '../chat-list.utils'
import type {
  BackgroundTask,
  ChatAssistantTurn,
  ChatMessage,
  ChatUserTurn,
  ToolCallBlock,
} from './types'
import type { RuntimeTranscriptSlice } from './runtime-projection'
import { createTranscriptHistory } from './transcript-history'
import { createTranscriptDecisions } from './transcript-decisions'

export interface TranscriptDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  rememberBackgroundTask: (task: BackgroundTask) => BackgroundTask
  applyPendingBackgroundEventsToTool: (block: ToolCallBlock) => void
  bumpFsChangedAtIfFsMutation: (message: UIMessage) => void
  fetchMessages: (botId: string, sessionId: string, options?: FetchMessagesOptions) => Promise<UITurn[]>
  locateMessage: (botId: string, sessionId: string, externalMessageId: string, before?: number, after?: number) => Promise<LocateMessageResult>
}

type SnapshotHook = (targetSessionId: string | undefined, turns: UITurn[]) => void
type RefreshAppliedHook = (targetSessionId: string, latestTimestamp?: string) => void

export interface LocateMessageResult {
  items: UITurn[]
  target_id?: string
}

// Owns the single active transcript view and every mutation of that view.
// Streams for inactive sessions may keep mutating their detached turn objects,
// but only this controller can add, remove, reconcile, or reorder visible turns.
export function createTranscriptController({
  currentBotId,
  sessionId,
  rememberBackgroundTask,
  applyPendingBackgroundEventsToTool,
  bumpFsChangedAtIfFsMutation,
  fetchMessages,
  locateMessage,
}: TranscriptDeps) {
  const messages = reactive<ChatMessage[]>([])
  const loadingMessages = ref(false)
  const loadingOlder = ref(false)
  const hasMoreOlder = ref(true)
  const hasLoadedOlder = ref(false)
  let onSnapshot: SnapshotHook = () => {}
  let onRefreshApplied: RefreshAppliedHook = () => {}
  let refreshPromise: { key: string; promise: Promise<void> } | null = null
  let historyGeneration = 0
  let loadingMessagesVersion = 0
  let loadingOlderVersion = 0

  function setSnapshotHook(hook: SnapshotHook) {
    onSnapshot = hook
  }

  function setRefreshAppliedHook(hook: RefreshAppliedHook) {
    onRefreshApplied = hook
  }

  const history = createTranscriptHistory({
    messages,
    sessionId,
    rememberBackgroundTask,
    applyPendingBackgroundEventsToTool,
    onSnapshot: (targetSessionId, turns) => onSnapshot(targetSessionId, turns),
    nextAssistantMessageId,
  })
  const {
    normalizeUIMessage,
    normalizeTurn,
    normalizeTurns,
    replaceMessages,
    mergeMessages,
    rememberAssistantError,
  } = history
  const {
    snapshotToolApprovalStates,
    assistantTurnForApproval,
    restoreToolApprovalStates,
    snapshotUserInputStates,
    assistantTurnForUserInput,
    restoreUserInputStates,
    markToolApprovalDecision,
    markUserInputDecision,
  } = createTranscriptDecisions(messages)

  const PAGE_SIZE = 30

  function isCurrentHistoryContext(botId: string, targetSessionId: string, generation: number): boolean {
    return generation === historyGeneration && isActiveSessionTarget(botId, targetSessionId)
  }

  function clearHistoryView(options: { hasMoreOlder?: boolean } = {}) {
    historyGeneration += 1
    loadingMessagesVersion += 1
    loadingOlderVersion += 1
    refreshPromise = null
    replaceMessages([])
    hasMoreOlder.value = options.hasMoreOlder === true
    hasLoadedOlder.value = false
    loadingMessages.value = false
    loadingOlder.value = false
  }

  function prepareForInitialization() {
    historyGeneration += 1
    loadingMessagesVersion += 1
    loadingOlderVersion += 1
    refreshPromise = null
    hasLoadedOlder.value = false
    loadingMessages.value = false
    loadingOlder.value = false
  }

  function markHistoryEmpty() {
    hasMoreOlder.value = false
    hasLoadedOlder.value = false
  }

  function replaceHistoryView(items: UITurn[], targetSessionId: string) {
    historyGeneration += 1
    loadingOlderVersion += 1
    refreshPromise = null
    replaceMessages(items, targetSessionId)
    hasMoreOlder.value = true
    hasLoadedOlder.value = false
    loadingOlder.value = false
  }

  async function refreshCurrentSession(targetBotId?: string, targetSessionId?: string) {
    const bid = (targetBotId ?? currentBotId.value ?? '').trim()
    const sid = (targetSessionId ?? sessionId.value ?? '').trim()
    if (!bid || !sid) return
    const key = `${bid}:${sid}`
    const generation = historyGeneration

    if (refreshPromise) {
      if (refreshPromise.key === key) {
        await refreshPromise.promise
        return
      }
      await refreshPromise.promise
    }

    const promise = (async () => {
      const turns = await fetchMessages(bid, sid, { limit: PAGE_SIZE })
      if (!isCurrentHistoryContext(bid, sid, generation)) return
      if (hasLoadedOlder.value) {
        mergeMessages(turns, sid)
      } else {
        replaceMessages(turns, sid)
        // The API pages raw DB rows but returns merged UI turns, so a short
        // page is not proof that history ended. Only pagination can settle it.
        hasMoreOlder.value = true
      }
      onRefreshApplied(sid, messages[messages.length - 1]?.timestamp)
    })().finally(() => {
      if (refreshPromise?.promise === promise) refreshPromise = null
    })
    refreshPromise = { key, promise }
    await promise
  }

  async function loadInitialMessages(botId: string, targetSessionId: string) {
    const bid = botId.trim()
    const sid = targetSessionId.trim()
    if (!bid || !sid) return
    loadingMessages.value = true
    const version = ++loadingMessagesVersion
    try {
      await refreshCurrentSession(bid, sid)
    } finally {
      if (version === loadingMessagesVersion) loadingMessages.value = false
    }
  }

  function fetchSessionWindow(botId: string, targetSessionId: string): Promise<UITurn[]> {
    return fetchMessages(botId, targetSessionId, { limit: PAGE_SIZE })
  }

  async function loadOlderMessages(): Promise<number> {
    const bid = (currentBotId.value ?? '').trim()
    const sid = (sessionId.value ?? '').trim()
    if (!bid || !sid || loadingOlder.value || !hasMoreOlder.value) return 0
    const first = messages[0]
    if (!first) return 0
    const firstId = serverMessageId(first)
    if (!firstId) return 0

    const generation = historyGeneration
    const version = ++loadingOlderVersion
    loadingOlder.value = true
    try {
      const maxDedupHops = 4
      let cursor = firstId
      for (let hop = 0; hop < maxDedupHops; hop++) {
        const turns = await fetchMessages(bid, sid, { limit: PAGE_SIZE, beforeMessageId: cursor })
        if (!isCurrentHistoryContext(bid, sid, generation)) return 0
        if (turns.length === 0) {
          hasMoreOlder.value = false
          return 0
        }

        const existingIds = new Set(messages.map(message => message.id))
        const normalized = normalizeTurns(turns, sid)
        const older = normalized.filter(turn => !existingIds.has(turn.id))
        if (older.length > 0) {
          prependToView(...older)
          hasLoadedOlder.value = true
          return older.length
        }

        const earliest = normalized[0] ? serverMessageId(normalized[0]) : ''
        if (!earliest || earliest === cursor) {
          hasMoreOlder.value = false
          return 0
        }
        cursor = earliest
      }
      hasMoreOlder.value = false
      return 0
    } catch (error) {
      console.error('Failed to load older messages:', error)
      return 0
    } finally {
      if (version === loadingOlderVersion) loadingOlder.value = false
    }
  }

  function findMessageIdByExternalId(externalMessageId: string): string | null {
    const target = externalMessageId.trim()
    if (!target) return null
    const found = messages.find(message =>
      (message.role === 'user' || message.role === 'assistant')
      && message.externalMessageId === target,
    )
    return found?.id ?? null
  }

  async function locateMessageByExternalId(externalMessageId: string): Promise<string | null> {
    const localID = findMessageIdByExternalId(externalMessageId)
    if (localID) return localID

    const bid = (currentBotId.value ?? '').trim()
    const sid = (sessionId.value ?? '').trim()
    const target = externalMessageId.trim()
    if (!bid || !sid || !target) return null
    const generation = historyGeneration

    try {
      const result = await locateMessage(bid, sid, target, PAGE_SIZE, PAGE_SIZE)
      if (!isCurrentHistoryContext(bid, sid, generation) || !result.items.length) return null
      mergeMessages(result.items, sid)
      hasMoreOlder.value = true
      hasLoadedOlder.value = true
      return result.target_id?.trim() || findMessageIdByExternalId(target)
    } catch (error) {
      console.error('Failed to locate message:', error)
      return null
    }
  }

  function isActiveSessionTarget(botId: string, targetSessionId: string): boolean {
    const bid = botId.trim()
    const sid = targetSessionId.trim()
    return Boolean(bid && sid && currentBotId.value === bid && sessionId.value === sid)
  }

  // Context-gated operations prevent a late stream or rollback for session A
  // from writing into the visible transcript after the user switches to B.
  function appendTurnToSession(botId: string, targetSessionId: string, turn: ChatMessage) {
    if (isActiveSessionTarget(botId, targetSessionId)) messages.push(turn)
  }

  function reattachTurnToSession(botId: string, targetSessionId: string, turn: ChatMessage) {
    if (!isActiveSessionTarget(botId, targetSessionId)) return
    if (messages.includes(turn)) return
    const adoptedIndex = messages.findIndex(message => message.id === turn.id)
    if (adoptedIndex >= 0) {
      messages.splice(adoptedIndex, 1, turn)
      return
    }
    const tailIndex = messages.length - 1
    const hydratedTail = messages[tailIndex]
    if (hydratedTail?.role === 'assistant' && !hydratedTail.streaming) {
      messages.splice(tailIndex, 1, turn)
      return
    }
    messages.push(turn)
  }

  function appendToView(...turns: ChatMessage[]) {
    messages.push(...turns)
  }

  function prependToView(...turns: ChatMessage[]) {
    messages.unshift(...turns)
  }

  function removeFromView(turn: ChatMessage) {
    const idx = messages.indexOf(turn)
    if (idx >= 0) messages.splice(idx, 1)
  }

  function removeTurnFromSession(botId: string, targetSessionId: string, turn: ChatMessage) {
    if (botId.trim() && targetSessionId.trim() && !isActiveSessionTarget(botId, targetSessionId)) return
    removeFromView(turn)
  }

  function findMessageIndexForReplacement(turn: ChatMessage): number {
    const referenceIndex = messages.indexOf(turn)
    if (referenceIndex >= 0) return referenceIndex
    const id = serverMessageId(turn)
    if (!id) return -1
    return messages.findIndex(message => serverMessageId(message) === id)
  }

  function replaceTailFromTurn(turn: ChatMessage, replacements: ChatMessage[]): ChatMessage[] {
    const idx = findMessageIndexForReplacement(turn)
    if (idx < 0) {
      appendToView(...replacements)
      return []
    }
    const replaced = messages.slice(idx)
    messages.splice(idx, messages.length - idx, ...replacements)
    return replaced
  }

  function restoreTailFromOptimistic(
    botId: string,
    targetSessionId: string,
    optimisticUserTurn: ChatUserTurn | null,
    assistantTurn: ChatAssistantTurn,
    replacedTurns: ChatMessage[],
  ) {
    if (!isActiveSessionTarget(botId, targetSessionId)) return
    const anchor = optimisticUserTurn ?? assistantTurn
    const idx = findMessageIndexForReplacement(anchor)
    if (idx >= 0) {
      messages.splice(idx, optimisticUserTurn ? 2 : 1, ...replacedTurns)
      return
    }
    if (optimisticUserTurn) removeTurnFromSession(botId, targetSessionId, optimisticUserTurn)
    removeTurnFromSession(botId, targetSessionId, assistantTurn)
    if (replacedTurns.length > 0) appendToView(...replacedTurns)
  }

  function createOptimisticAssistantTurn(invocationId = ''): ChatAssistantTurn {
    return {
      id: nextId(),
      role: 'assistant',
      messages: [],
      timestamp: new Date().toISOString(),
      streaming: true,
      __optimistic: true,
      invocationId: invocationId.trim() || undefined,
    }
  }

  function createOptimisticUserTurn(
    text: string,
    attachments?: ChatAttachment[],
    invocationId = '',
  ): ChatUserTurn {
    return {
      id: nextId(),
      role: 'user',
      text,
      attachments: (attachments ?? []).map(attachment => ({
        type: attachment.type,
        base64: attachment.base64,
        name: attachment.name ?? '',
        mime: attachment.mime ?? '',
      })),
      timestamp: new Date().toISOString(),
      streaming: false,
      isSelf: true,
      __optimistic: true,
      invocationId: invocationId.trim() || undefined,
    }
  }

  function bindRuntimeTurn(invocationId: string, turnId: string, runId: string) {
    const invocation = invocationId.trim()
    const turn = turnId.trim()
    const run = runId.trim()
    if (!invocation || !turn || !run) return
    for (const message of messages) {
      if (message.role === 'system' || message.invocationId !== invocation) continue
      message.runtimeTurnId = turn
      message.runtimeRunId = run
    }
  }

  function runtimeMessage(
    normalized: ChatUserTurn | ChatAssistantTurn,
    slice: RuntimeTranscriptSlice,
  ): ChatUserTurn | ChatAssistantTurn {
    normalized.runtimeTurnId = slice.turnId
    normalized.runtimeRunId = slice.runId
    normalized.__optimistic = false
    if (normalized.role === 'assistant') normalized.streaming = slice.streaming
    return normalized
  }

  function applyRuntimeTranscript(slice: RuntimeTranscriptSlice) {
    if (!slice.turnId || slice.turns.length === 0) return
    const incoming = slice.turns
      .map(normalizeTurn)
      .filter((turn): turn is ChatUserTurn | ChatAssistantTurn => turn.role !== 'system')
      .map(turn => runtimeMessage(turn, slice))
    const existing = messages.filter((turn): turn is ChatUserTurn | ChatAssistantTurn =>
      turn.role !== 'system'
      && turn.runtimeTurnId === slice.turnId,
    )
    const resolved = (['user', 'assistant'] as const).flatMap((role) => {
      const current = existing.find(turn => turn.role === role)
      const next = incoming.find(turn => turn.role === role)
      if (!next) return current ? [current] : []
      if (!current) return [next]
      const renderId = current.id
      Object.assign(current, next, { id: renderId })
      return [current]
    })

    const operationAnchor = slice.operation?.replace_from_message_id?.trim() ?? ''
    if (operationAnchor && existing.length === 0) {
      const anchor = messages.find(turn => serverMessageId(turn) === operationAnchor)
      if (anchor) replaceTailFromTurn(anchor, resolved)
      else appendToView(...resolved)
      return
    }

    if (existing.length === 0) {
      appendToView(...resolved)
      return
    }
    const indices = existing
      .map(turn => messages.indexOf(turn))
      .filter(index => index >= 0)
      .sort((left, right) => left - right)
    const insertAt = indices[0] ?? messages.length
    for (let index = indices.length - 1; index >= 0; index -= 1) {
      messages.splice(indices[index]!, 1)
    }
    messages.splice(insertAt, 0, ...resolved)
  }

  // Tool updates are partial snapshots. Preserve fields that an earlier stream
  // already filled, and never let a stale pending approval undo a local decision.
  function mergeToolCallBlock(existing: ToolCallBlock, incoming: ToolCallBlock) {
    Object.assign(existing, incoming, {
      id: existing.id,
      name: incoming.name || existing.name,
      toolName: incoming.toolName || existing.toolName,
      input: incoming.input ?? existing.input,
      result: incoming.result ?? existing.result,
      output: incoming.output ?? existing.output,
      approval: mergeApprovalState(existing.approval, incoming.approval),
      execution_location: incoming.execution_location ?? existing.execution_location,
      userInput: incoming.userInput ?? existing.userInput,
      user_input: incoming.user_input ?? existing.user_input,
      backgroundTask: incoming.backgroundTask ?? existing.backgroundTask,
      background_task: incoming.background_task ?? existing.background_task,
      progress: incoming.progress ?? existing.progress,
    })
  }

  function upsertAssistantUIMessage(turn: ChatAssistantTurn, message: UIMessage) {
    const normalized = normalizeUIMessage(message)
    if (normalized.type === 'tool' && normalized.toolCallId) {
      const existing = turn.messages.find((block): block is ToolCallBlock =>
        block.type === 'tool' && block.toolCallId === normalized.toolCallId,
      )
      if (existing) {
        mergeToolCallBlock(existing, normalized)
        bumpFsChangedAtIfFsMutation(message)
        return
      }
    }
    turn.messages = upsertById(turn.messages, normalized)
    bumpFsChangedAtIfFsMutation(message)
  }

  function nextAssistantMessageId(turn: ChatAssistantTurn): number {
    return turn.messages.reduce((maxId, message) => Math.max(maxId, message.id), -1) + 1
  }

  function hasVisibleAssistantBlocks(turn: ChatAssistantTurn): boolean {
    return turn.messages.some(block => block.type !== 'error')
  }

  function finishAssistantTurn(turn: ChatAssistantTurn) {
    turn.streaming = false
  }

  function appendAssistantError(assistantTurn: ChatAssistantTurn, targetSessionId: string, errorMessage: string) {
    const text = errorMessage.trim()
    if (!text) return
    rememberAssistantError(text, targetSessionId, assistantTurn)
    assistantTurn.messages.push({ id: nextAssistantMessageId(assistantTurn), type: 'error', content: text })
  }

  function finalizeStreamFailure(assistantTurn: ChatAssistantTurn, botId: string, targetSessionId: string, error: Error) {
    if (!hasVisibleAssistantBlocks(assistantTurn)) {
      const runtimeTurnId = assistantTurn.runtimeTurnId?.trim()
      if (runtimeTurnId) {
        for (let index = messages.length - 1; index >= 0; index -= 1) {
          const turn = messages[index]
          if (!turn) continue
          if (
            turn.role !== 'system'
            && turn.runtimeTurnId === runtimeTurnId
          ) messages.splice(index, 1)
        }
        return
      }
      removeTurnFromSession(botId, targetSessionId, assistantTurn)
      return
    }
    if (error.name === 'AbortError') return
    if (assistantTurn.messages.some(block => block.type === 'error')) return
    appendAssistantError(assistantTurn, targetSessionId, error.message)
  }

  function latestOptimisticUserText(): string {
    for (let i = messages.length - 1; i >= 0; i -= 1) {
      const message = messages[i]
      if (message?.role === 'user') return message.text.trim()
    }
    return ''
  }

  function hasTurn(turn: ChatMessage): boolean {
    return messages.includes(turn)
  }

  function findTurnByServerId(messageId: string): ChatMessage | null {
    const id = messageId.trim()
    if (!id) return null
    return messages.find(turn => serverMessageId(turn) === id) ?? null
  }

  function latestVisibleTurn(role: 'user'): ChatUserTurn | null
  function latestVisibleTurn(role: 'assistant'): ChatAssistantTurn | null
  function latestVisibleTurn(role: ChatMessage['role']): ChatUserTurn | ChatAssistantTurn | null {
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const turn = messages[index]
      if (
        turn
        && turn.role !== 'system'
        && turn.role === role
        && !turn.__optimistic
      ) return turn
    }
    return null
  }

  function isLatestVisibleUserTurn(turn: ChatMessage): turn is ChatUserTurn {
    if (turn.role !== 'user') return false
    const latest = latestVisibleTurn('user')
    return Boolean(latest && serverMessageId(latest) === serverMessageId(turn))
  }

  function isLatestVisibleAssistantTurn(turn: ChatMessage): turn is ChatAssistantTurn {
    if (turn.role !== 'assistant') return false
    const latest = latestVisibleTurn('assistant')
    return Boolean(latest && serverMessageId(latest) === serverMessageId(turn))
  }

  function resetUserScope() {
    clearHistoryView({ hasMoreOlder: true })
    history.reset()
  }

  return {
    messages,
    loadingMessages,
    loadingOlder,
    hasMoreOlder,
    hasLoadedOlder,
    setSnapshotHook,
    setRefreshAppliedHook,
    normalizeUIMessage,
    normalizeTurn,
    normalizeTurns,
    replaceMessages,
    mergeMessages,
    clearHistoryView,
    prepareForInitialization,
    markHistoryEmpty,
    replaceHistoryView,
    refreshCurrentSession,
    loadInitialMessages,
    fetchSessionWindow,
    loadOlderMessages,
    findMessageIdByExternalId,
    locateMessageByExternalId,
    isActiveSessionTarget,
    appendTurnToSession,
    reattachTurnToSession,
    appendToView,
    prependToView,
    removeFromView,
    removeTurnFromSession,
    replaceTailFromTurn,
    restoreTailFromOptimistic,
    createOptimisticAssistantTurn,
    createOptimisticUserTurn,
    bindRuntimeTurn,
    applyRuntimeTranscript,
    upsertAssistantUIMessage,
    hasVisibleAssistantBlocks,
    finishAssistantTurn,
    snapshotToolApprovalStates,
    assistantTurnForApproval,
    restoreToolApprovalStates,
    snapshotUserInputStates,
    assistantTurnForUserInput,
    restoreUserInputStates,
    finalizeStreamFailure,
    latestOptimisticUserText,
    hasTurn,
    findTurnByServerId,
    isLatestVisibleUserTurn,
    isLatestVisibleAssistantTurn,
    markToolApprovalDecision,
    markUserInputDecision,
    resetUserScope,
  }
}
