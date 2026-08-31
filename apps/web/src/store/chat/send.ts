import type { Ref } from 'vue'
import { parseMemohError, resolveApiErrorMessage } from '@/utils/api-error'
import type {
  ChatAttachment,
  RequestedSkillSelection,
  WSClientMessage,
} from '@/composables/api/useChat'
import {
  cloneRequestedSkills,
  createInvocationId,
  hasUserAttachments,
  normalizeRequestedSkills,
  requestedSkillRequestsForWire,
} from '../chat-list.normalize'
import type { ChatViewEntry } from './view-registry'
import type { createTranscriptController } from './transcript'
import type {
  ChatAssistantTurn,
  ChatMessage,
  ChatUserTurn,
  ChatViewTarget,
  SendMessageOptions,
  SendMessageResult,
  SendMessageStage,
} from './types'

type Transcript = ReturnType<typeof createTranscriptController>

export type WebCommandResult =
  | { kind: 'none' }
  | { kind: 'handled' }
  | { kind: 'error'; message: string }

export interface StartupSendFailure {
  id: string
  botId: string
  sessionId: string
  composerScope?: string
  error: string
  restoreInput: string
  restoreAttachments?: ChatAttachment[]
  restoreRequestedSkills?: RequestedSkillSelection[]
}

export class StreamFailureError extends Error {
  stage: SendMessageStage
  feedback?: unknown

  constructor(message: string, stage: SendMessageStage, feedback?: unknown) {
    super(message)
    this.name = 'StreamFailureError'
    this.stage = stage
    this.feedback = feedback
  }
}

export class CommandStreamError extends StreamFailureError {
  constructor(message: string) {
    super(message, 'startup')
    this.name = 'CommandStreamError'
  }
}

interface TrackStreamInput {
  invocationId: string
  assistantTurn: ChatAssistantTurn
  botId: string
  sessionId: string
  composerScope?: string
  viewId?: string
}

export interface ChatSendDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  focusedChatViewId: Ref<string>
  overrideModelId: Ref<string>
  overrideReasoningEffort: Ref<string>
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
  chatView: (target?: Partial<ChatViewTarget>) => ChatViewEntry
  transcriptForTarget: (target?: Partial<ChatViewTarget>) => Transcript
  isWebSlashInput: (text: string) => boolean
  quickActionIDForSlash: (text: string) => string
  isExternalAgentTarget: (target: ChatViewTarget) => boolean
  handleWebNewCommand: (
    text: string,
    attachments: ChatAttachment[] | undefined,
    target: ChatViewTarget,
  ) => Promise<WebCommandResult>
  handleWebSlashCommand: (
    text: string,
    hasRequestedSkills: boolean,
    composerScope: string,
    target: ChatViewTarget,
  ) => Promise<WebCommandResult>
  commandErrorMessage: (code: string) => string
  showCommandError: (
    code: string,
    message: string,
    scope: { botId: string; sessionId?: string; composerScope?: string },
  ) => void
  clearCommandEvent: (scope: {
    botId: string
    sessionId?: string
    composerScope?: string
  }) => void
  chatReadOnlyFor: (target: ChatViewTarget) => boolean
  isChatViewStreaming: (target: ChatViewTarget, composerScope?: string) => boolean
  isChatViewCreatingSession: (target: ChatViewTarget) => boolean
  pendingExternalAgentStateFor: (target: ChatViewTarget) => unknown
  ensureChatViewSession: (
    target: ChatViewTarget,
    firstPrompt?: string,
  ) => Promise<ChatViewTarget>
  startSessionRuntime: (botId: string, sessionId: string) => void
  recordUserSent: (target: ChatViewTarget, sessionId: string, wasDraft: boolean) => void
  // True when a socket handle exists for the bot; the ws layer queues until
  // open, so sends are not refused during the first-open handshake.
  ensureWebSocket: (botId: string) => boolean
  trackAssistantStream: (input: TrackStreamInput) => Promise<void>
  sendWebSocketMessage: (botId: string, message: WSClientMessage) => boolean
  createdSessionIdForInvocation: (invocationId: string) => string
  forgetCreatedSession: (invocationId: string) => void
  refreshCurrentSession: (botId: string, sessionId: string) => Promise<void>
  hasVisibleAssistantBlocks: (turn: ChatAssistantTurn) => boolean
  finalizeStreamFailure: (
    assistantTurn: ChatAssistantTurn,
    botId: string,
    sessionId: string,
    error: Error,
  ) => void
  removeTurnFromSession: (
    botId: string,
    sessionId: string,
    turn: ChatMessage,
  ) => void
  cleanupFailedDeferredSession: (
    botId: string,
    sessionId: string,
    composerScope: string,
  ) => Promise<void>
  discardAssistantStream: (invocationId: string) => void
  rememberStartupSendFailure: (failure: Omit<StartupSendFailure, 'id'>) => void
  sendFailedMessage: () => string
  updateForkAnchorForReplacedMessage: (
    sessionId: string,
    target: ChatMessage,
    messages: ChatMessage[],
  ) => (() => void) | null | undefined
  restoreTailFromOptimistic: (
    botId: string,
    sessionId: string,
    userTurn: ChatUserTurn | null,
    assistantTurn: ChatAssistantTurn,
    replacedTurns: ChatMessage[],
  ) => void
}

export function createChatSend(deps: ChatSendDeps) {
  async function sendMessage(
    text: string,
    attachments?: ChatAttachment[],
    options: SendMessageOptions = {},
  ): Promise<SendMessageResult> {
    const trimmed = text.trim()
    const requestedSkills = normalizeRequestedSkills(options.requestedSkills)
    let viewTarget = deps.normalizeTarget(options.target)
    const composerScope = options.composerScope?.trim()
      || (options.target ? `${viewTarget.botId}:${viewTarget.viewId}` : 'chat')
    const commandScope = {
      botId: viewTarget.botId,
      sessionId: viewTarget.sessionId ?? undefined,
      composerScope,
    }
    const isExternalAgent = deps.isExternalAgentTarget(viewTarget)
    if (!trimmed && !attachments?.length && requestedSkills.length === 0) {
      return { ok: false, stage: 'startup' }
    }

    if (requestedSkills.length > 0 && deps.isWebSlashInput(trimmed)) {
      const message = deps.commandErrorMessage('invalid_skill_slash_syntax')
      deps.showCommandError('invalid_skill_slash_syntax', message, commandScope)
      return {
        ok: false,
        stage: 'startup',
        error: message,
        restoreInput: text,
        restoreAttachments: attachments,
        restoreRequestedSkills: cloneRequestedSkills(requestedSkills),
      }
    }

    if (
      deps.isWebSlashInput(trimmed)
      && attachments?.length
      && (!isExternalAgent || deps.quickActionIDForSlash(trimmed) !== '')
    ) {
      const message = deps.commandErrorMessage('slash_attachments_unsupported')
      deps.showCommandError('slash_attachments_unsupported', message, commandScope)
      return {
        ok: false,
        stage: 'startup',
        error: message,
        restoreInput: text,
        restoreAttachments: attachments,
        restoreRequestedSkills: cloneRequestedSkills(requestedSkills),
      }
    }

    const newCommand = await deps.handleWebNewCommand(trimmed, attachments, viewTarget)
    if (newCommand.kind === 'handled') return { ok: true }
    if (newCommand.kind === 'error') {
      return {
        ok: false,
        stage: 'startup',
        error: newCommand.message,
        restoreInput: text,
        restoreAttachments: attachments,
        restoreRequestedSkills: cloneRequestedSkills(requestedSkills),
      }
    }
    const slashCommand = await deps.handleWebSlashCommand(
      trimmed,
      requestedSkills.length > 0,
      composerScope,
      viewTarget,
    )
    if (slashCommand.kind === 'handled') return { ok: true }
    if (slashCommand.kind === 'error') {
      return {
        ok: false,
        stage: 'startup',
        error: slashCommand.message,
        restoreInput: text,
        restoreAttachments: attachments,
        restoreRequestedSkills: cloneRequestedSkills(requestedSkills),
      }
    }
    if (viewTarget.sessionId && deps.chatReadOnlyFor(viewTarget)) {
      return { ok: false, stage: 'startup' }
    }
    deps.clearCommandEvent(commandScope)
    const initialView = deps.chatView(viewTarget)
    if (
      deps.isChatViewStreaming(viewTarget, composerScope)
      || deps.isChatViewCreatingSession(viewTarget)
      || initialView.transcript.loadingMessages.value
      || !viewTarget.botId
    ) return { ok: false, stage: 'startup' }

    let assistantTurn: ChatAssistantTurn | null = null
    let userTurn: ChatUserTurn | null = null
    let sendBotId = ''
    let sendSessionId = ''
    let sendInvocationId = ''
    let turnAppendStarted = false

    const wasDraft = !viewTarget.sessionId
    const serverSlashActivation = deps.isWebSlashInput(trimmed)
      && deps.quickActionIDForSlash(trimmed) === ''
      && !isExternalAgent
    const serverSkillActivation = requestedSkills.length > 0 || serverSlashActivation
    if (serverSkillActivation && wasDraft && deps.pendingExternalAgentStateFor(viewTarget)) {
      const message = deps.commandErrorMessage('unsupported_skill_slash_context')
      deps.showCommandError('unsupported_skill_slash_context', message, commandScope)
      return {
        ok: false,
        stage: 'startup',
        error: message,
        restoreInput: text,
        restoreAttachments: attachments,
        restoreRequestedSkills: cloneRequestedSkills(requestedSkills),
        composerScope,
      }
    }

    const deferSessionCreation = serverSkillActivation && wasDraft
    try {
      const modelId = options.modelId?.trim() || deps.overrideModelId.value || undefined
      const reasoningEffort = options.reasoningEffort?.trim()
        || deps.overrideReasoningEffort.value
        || undefined
      if (!deferSessionCreation) {
        viewTarget = await deps.ensureChatViewSession(viewTarget, wasDraft ? trimmed : undefined)
      }

      const botId = viewTarget.botId
      const targetSessionId = viewTarget.sessionId ?? ''
      if (!targetSessionId && !deferSessionCreation) throw new Error('Session not selected')
      sendBotId = botId
      sendSessionId = targetSessionId
      sendInvocationId = createInvocationId()
      const transcript = deps.transcriptForTarget(viewTarget)
      if (targetSessionId) {
        deps.startSessionRuntime(botId, targetSessionId)
        deps.recordUserSent(viewTarget, targetSessionId, wasDraft)
      }

      assistantTurn = transcript.createOptimisticAssistantTurn(sendInvocationId)
      turnAppendStarted = true
      options.onBeforeTurnAppend?.()
      if (!serverSkillActivation) {
        userTurn = transcript.createOptimisticUserTurn(
          trimmed,
          attachments,
          sendInvocationId,
        )
        transcript.appendToView(userTurn, assistantTurn)
      }

      if (!deps.ensureWebSocket(botId)) {
        throw new StreamFailureError('WebSocket is not connected', 'startup')
      }
      const completion = deps.trackAssistantStream({
        invocationId: sendInvocationId,
        assistantTurn,
        botId,
        sessionId: targetSessionId,
        composerScope,
        viewId: viewTarget.viewId,
      })
      if (!deps.sendWebSocketMessage(botId, {
        type: 'message',
        invocation_id: sendInvocationId,
        composer_scope: composerScope,
        text: trimmed,
        session_id: targetSessionId || undefined,
        attachments,
        requested_skills: requestedSkills.length
          ? requestedSkillRequestsForWire(requestedSkills)
          : undefined,
        model_id: modelId,
        reasoning_effort: reasoningEffort,
        workspace_target_id: options.workspaceTargetId?.trim() || undefined,
      })) throw new StreamFailureError('WebSocket is not connected', 'startup')
      await completion
      const createdSessionId = deps.createdSessionIdForInvocation(sendInvocationId)
      const fallbackActiveSessionId = !options.target
        && (deps.currentBotId.value ?? '').trim() === botId
        ? deps.sessionId.value ?? ''
        : ''
      const refreshSessionId = sendSessionId || createdSessionId || fallbackActiveSessionId
      deps.forgetCreatedSession(sendInvocationId)
      if (refreshSessionId) await deps.refreshCurrentSession(botId, refreshSessionId)

      return { ok: true }
    } catch (error) {
      const failure = error instanceof Error ? error : new Error('Unknown error')
      const isAbort = failure.name === 'AbortError'
      const isCommandError = failure instanceof CommandStreamError
      const reason = resolveApiErrorMessage(error, failure.message || deps.sendFailedMessage())
      const errorCode = parseMemohError(error)?.code
      const stage: SendMessageStage = failure instanceof StreamFailureError
        ? failure.stage
        : (assistantTurn && deps.hasVisibleAssistantBlocks(assistantTurn) ? 'stream' : 'startup')
      const createdSessionId = sendInvocationId
        ? deps.createdSessionIdForInvocation(sendInvocationId)
        : ''
      const botId = sendBotId || viewTarget.botId || deps.currentBotId.value || ''
      const targetSessionId = sendSessionId || createdSessionId

      if (assistantTurn) {
        deps.finalizeStreamFailure(assistantTurn, botId, targetSessionId, failure)
      }
      if (!isAbort && stage === 'startup' && userTurn) {
        deps.removeTurnFromSession(botId, targetSessionId, userTurn)
      }
      if (
        !isAbort
        && stage === 'startup'
        && deferSessionCreation
        && wasDraft
        && createdSessionId
      ) {
        await deps.cleanupFailedDeferredSession(botId, createdSessionId, composerScope)
      }

      if (sendInvocationId) deps.discardAssistantStream(sendInvocationId)
      if (sendInvocationId) deps.forgetCreatedSession(sendInvocationId)
      if (!isAbort && stage === 'startup' && turnAppendStarted) {
        options.onTurnAppendAborted?.()
      }

      if (isAbort) return { ok: false, stage: 'stream', error: reason, errorCode }
      if (stage === 'startup') {
        const currentBotId = (deps.currentBotId.value ?? '').trim()
        const currentSessionId = (deps.sessionId.value ?? '').trim()
        const restoredOriginalDraft = deferSessionCreation
          && wasDraft
          && !currentSessionId
          && deps.focusedChatViewId.value === viewTarget.viewId
        const stillCurrent = currentBotId === botId
          && (
            !targetSessionId
            || currentSessionId === targetSessionId
            || restoredOriginalDraft
          )
        const deferredDraftStillCurrent = !(
          deferSessionCreation
          && wasDraft
          && currentSessionId
        )
        const commandErrorRestoredDraft = isCommandError
          && deferSessionCreation
          && wasDraft
          && !currentSessionId
        if (
          stillCurrent
          && deferredDraftStillCurrent
          && (!isCommandError || commandErrorRestoredDraft)
        ) {
          deps.rememberStartupSendFailure({
            botId,
            sessionId: targetSessionId,
            composerScope,
            error: reason,
            restoreInput: text,
            restoreAttachments: attachments,
            restoreRequestedSkills: cloneRequestedSkills(requestedSkills),
          })
        }
        return {
          ok: false,
          stage,
          error: reason,
          errorCode,
          restoreInput: text,
          restoreAttachments: attachments,
          restoreRequestedSkills: cloneRequestedSkills(requestedSkills),
          composerScope,
        }
      }
      return { ok: false, stage, error: reason, errorCode }
    }
  }

  async function retryLatestAssistant(
    turnId: string,
    options: {
      target?: ChatViewTarget
      modelId?: string
      reasoningEffort?: string
      workspaceTargetId?: string
    } = {},
  ): Promise<SendMessageResult> {
    const viewTarget = deps.normalizeTarget(options.target)
    const botId = viewTarget.botId
    const targetSessionId = viewTarget.sessionId ?? ''
    const transcript = deps.transcriptForTarget(viewTarget)
    const targetTurnId = turnId.trim()
    if (
      !botId
      || !targetSessionId
      || !targetTurnId
      || deps.chatReadOnlyFor(viewTarget)
      || deps.isChatViewStreaming(viewTarget)
      || transcript.loadingMessages.value
    ) return { ok: false, stage: 'startup' }
    const target = transcript.findTurnByTurnId(targetTurnId, 'assistant')
    if (!target || !transcript.isLatestVisibleAssistantTurn(target)) {
      return { ok: false, stage: 'startup' }
    }

    const invocationId = createInvocationId()
    const assistantTurn = transcript.createOptimisticAssistantTurn(invocationId)
    const restoreForkAnchor = deps.updateForkAnchorForReplacedMessage(
      targetSessionId,
      target,
      transcript.messages,
    )
    const replacedTurns = transcript.replaceTailFromTurn(target, [assistantTurn])
    try {
      if (!deps.ensureWebSocket(botId)) {
        throw new StreamFailureError('WebSocket is not connected', 'startup')
      }
      const completion = deps.trackAssistantStream({
        invocationId,
        assistantTurn,
        botId,
        sessionId: targetSessionId,
      })
      if (!deps.sendWebSocketMessage(botId, {
        type: 'retry_message',
        invocation_id: invocationId,
        session_id: targetSessionId,
        turn_id: targetTurnId,
        model_id: options.modelId?.trim() || deps.overrideModelId.value || undefined,
        reasoning_effort: options.reasoningEffort?.trim()
          || deps.overrideReasoningEffort.value
          || undefined,
        workspace_target_id: options.workspaceTargetId?.trim() || undefined,
      })) throw new StreamFailureError('WebSocket is not connected', 'startup')
      await completion
      await deps.refreshCurrentSession(botId, targetSessionId)
      return { ok: true }
    } catch (error) {
      const failure = error instanceof Error ? error : new Error('Unknown error')
      const reason = resolveApiErrorMessage(error, failure.message || deps.sendFailedMessage())
      const errorCode = parseMemohError(error)?.code
      const stage: SendMessageStage = failure instanceof StreamFailureError
        ? failure.stage
        : (deps.hasVisibleAssistantBlocks(assistantTurn) ? 'stream' : 'startup')
      deps.discardAssistantStream(invocationId)
      if (stage === 'startup') {
        restoreForkAnchor?.()
        deps.restoreTailFromOptimistic(
          botId,
          targetSessionId,
          null,
          assistantTurn,
          replacedTurns,
        )
      } else {
        deps.finalizeStreamFailure(assistantTurn, botId, targetSessionId, failure)
      }
      return { ok: false, stage, error: reason, errorCode }
    }
  }

  async function editLatestUser(
    turnId: string,
    text: string,
    options: {
      target?: ChatViewTarget
      modelId?: string
      reasoningEffort?: string
      workspaceTargetId?: string
    } = {},
  ): Promise<SendMessageResult> {
    const trimmed = text.trim()
    const viewTarget = deps.normalizeTarget(options.target)
    const botId = viewTarget.botId
    const targetSessionId = viewTarget.sessionId ?? ''
    const transcript = deps.transcriptForTarget(viewTarget)
    const targetTurnId = turnId.trim()
    if (
      !botId
      || !targetSessionId
      || !targetTurnId
      || !trimmed
      || deps.chatReadOnlyFor(viewTarget)
      || deps.isChatViewStreaming(viewTarget)
      || transcript.loadingMessages.value
    ) return { ok: false, stage: 'startup' }
    const target = transcript.findTurnByTurnId(targetTurnId, 'user')
    if (!target || !transcript.isLatestVisibleUserTurn(target) || hasUserAttachments(target)) {
      return { ok: false, stage: 'startup' }
    }

    const invocationId = createInvocationId()
    const userTurn = transcript.createOptimisticUserTurn(trimmed, undefined, invocationId)
    const assistantTurn = transcript.createOptimisticAssistantTurn(invocationId)
    const restoreForkAnchor = deps.updateForkAnchorForReplacedMessage(
      targetSessionId,
      target,
      transcript.messages,
    )
    const replacedTurns = transcript.replaceTailFromTurn(target, [userTurn, assistantTurn])
    try {
      if (!deps.ensureWebSocket(botId)) {
        throw new StreamFailureError('WebSocket is not connected', 'startup')
      }
      const completion = deps.trackAssistantStream({
        invocationId,
        assistantTurn,
        botId,
        sessionId: targetSessionId,
      })
      if (!deps.sendWebSocketMessage(botId, {
        type: 'edit_message',
        invocation_id: invocationId,
        session_id: targetSessionId,
        turn_id: targetTurnId,
        text: trimmed,
        model_id: options.modelId?.trim() || deps.overrideModelId.value || undefined,
        reasoning_effort: options.reasoningEffort?.trim()
          || deps.overrideReasoningEffort.value
          || undefined,
        workspace_target_id: options.workspaceTargetId?.trim() || undefined,
      })) throw new StreamFailureError('WebSocket is not connected', 'startup')
      await completion
      await deps.refreshCurrentSession(botId, targetSessionId)
      return { ok: true }
    } catch (error) {
      const failure = error instanceof Error ? error : new Error('Unknown error')
      const reason = resolveApiErrorMessage(error, failure.message || deps.sendFailedMessage())
      const errorCode = parseMemohError(error)?.code
      const stage: SendMessageStage = failure instanceof StreamFailureError
        ? failure.stage
        : (deps.hasVisibleAssistantBlocks(assistantTurn) ? 'stream' : 'startup')
      deps.discardAssistantStream(invocationId)
      if (stage === 'startup') {
        restoreForkAnchor?.()
        deps.restoreTailFromOptimistic(
          botId,
          targetSessionId,
          userTurn,
          assistantTurn,
          replacedTurns,
        )
      } else {
        deps.finalizeStreamFailure(assistantTurn, botId, targetSessionId, failure)
      }
      return { ok: false, stage, error: reason, errorCode, restoreInput: text }
    }
  }

  return { sendMessage, retryLatestAssistant, editLatestUser }
}
