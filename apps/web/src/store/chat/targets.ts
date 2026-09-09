import { computed, type Ref } from 'vue'
import type { SessionSummary } from '@/composables/api/useChat'
import { isAgentRuntimeType, normalizedRuntimeType } from '../chat-list.utils'
import type {
  ActiveChatTarget,
  ChatViewTarget,
} from './types'

export function createChatTargets(deps: {
  currentBotId: Ref<string | null>
  focusedViewId: Ref<string>
  explicitSessionSelection: Ref<boolean>
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
  knownSession: (sessionId: string) => SessionSummary | null | undefined
  pendingExternalAgentState: (target: ChatViewTarget) => {
    input: {
      runtime?: 'acp' | 'codex' | 'claude-code'
    }
    metadata: Record<string, unknown>
  } | null | undefined
}) {
  function sessionMetadata(session: SessionSummary | null): Record<string, unknown> {
    if (!session) return {}
    return {
      ...(session.metadata && typeof session.metadata === 'object'
        ? session.metadata
        : {}),
      ...(session.runtime_metadata && typeof session.runtime_metadata === 'object'
        ? session.runtime_metadata
        : {}),
    }
  }

  function targetFor(target?: ChatViewTarget): ActiveChatTarget {
    const resolved = deps.normalizeTarget(target)
    const focused = resolved.viewId === deps.focusedViewId.value
      && resolved.botId === (deps.currentBotId.value ?? '').trim()
    const explicitSelection = focused
      ? deps.explicitSessionSelection.value
      : false
    const sessionId = (resolved.sessionId ?? '').trim()
    if (sessionId) {
      const session = deps.knownSession(sessionId) ?? null
      const runtimeType = session ? normalizedRuntimeType(session) : 'unknown'
      return {
        kind: 'session',
        sessionId,
        session,
        runtimeType,
        isExternalAgent: isAgentRuntimeType(runtimeType),
        isPendingExternalAgent: false,
        metadata: sessionMetadata(session),
        explicitSelection,
      }
    }

    const pendingState = deps.pendingExternalAgentState(resolved)
    if (pendingState) {
      const runtimeType = pendingState.input.runtime === 'codex'
        || pendingState.input.runtime === 'claude-code'
        ? pendingState.input.runtime
        : 'acp_agent'
      return {
        kind: 'draft-external-agent',
        sessionId: null,
        session: null,
        runtimeType,
        isExternalAgent: true,
        isPendingExternalAgent: true,
        metadata: pendingState.metadata,
        explicitSelection,
      }
    }

    return {
      kind: 'draft-native',
      sessionId: null,
      session: null,
      runtimeType: 'model',
      isExternalAgent: false,
      isPendingExternalAgent: false,
      metadata: {},
      explicitSelection,
    }
  }

  const activeTarget = computed<ActiveChatTarget>(() => targetFor())

  function readOnlyFor(target: ChatViewTarget) {
    const resolved = deps.normalizeTarget(target)
    const session = targetFor(resolved).session
    if (!session) return Boolean(resolved.sessionId)
    const type = session.type ?? 'chat'
    if (type === 'schedule') {
      return true
    }
    const channelType = (session.channel_type ?? '').trim().toLowerCase()
    return Boolean(channelType && channelType !== 'local')
  }

  function canForkFor(target: ChatViewTarget) {
    return targetFor(target).session?.type === 'chat'
  }

  return {
    targetFor,
    activeTarget,
    readOnlyFor,
    canForkFor,
  }
}
