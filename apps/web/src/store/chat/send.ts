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
import type { FirstSendTracker } from './first-send'
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
  // The send ended without an answer from the server (the socket dropped, or
  // no confirmation came in time), so the server may have taken the run.
  outcomeUnknown: boolean

  constructor(message: string, stage: SendMessageStage, feedback?: unknown, options: { outcomeUnknown?: boolean } = {}) {
    super(message)
    this.name = 'StreamFailureError'
    this.stage = stage
    this.feedback = feedback
    this.outcomeUnknown = options.outcomeUnknown === true
  }
}

// failureStage is how far a failed turn got. A new send the server accepted
// (bindRunId stamped its run on the turn) is in the history with its failure,
// so nothing returns to the composer. A retry or edit replaces a turn the
// history already has and counts as streamed only once the caller saw output.
export function failureStage(assistantTurn: ChatAssistantTurn, replacesTurn: boolean, replacementHasOutput: boolean): SendMessageStage {
  if (replacesTurn) return replacementHasOutput ? 'stream' : 'startup'
  return assistantTurn.runtimeRunId?.trim() ? 'stream' : 'startup'
}

export class CommandStreamError extends StreamFailureError {
  constructor(message: string, feedback?: unknown) {
    super(message, 'startup', feedback)
    this.name = 'CommandStreamError'
  }
}

interface TrackStreamInput {
  onModelPreferenceSettled?: () => void
  invocationId: string
  assistantTurn: ChatAssistantTurn
  replacesTurn?: boolean
  botId: string
  sessionId: string
  composerScope?: string
  viewId?: string
}

export interface ChatSendDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  focusedChatViewId: Ref<string>
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
    keepTurn?: boolean,
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
  // The workdir a native draft is bound to; sent with its first message.
  draftWorkdirIdFor: (botId: string) => string
  firstSend: Pick<FirstSendTracker, 'begin' | 'admit' | 'reveal' | 'finish' | 'isRevealed'>
  // Starts the limit on how long an in-band first send waits for the server
  // to confirm it; returns its cancel.
  watchFirstSendConfirmation: (invocationId: string) => () => void
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
    // A send refused before it started hands the input back to the composer.
    const refused = (error: string, scope?: string): SendMessageResult => ({
      ok: false,
      stage: 'startup',
      error,
      restoreInput: text,
      restoreAttachments: attachments,
      restoreRequestedSkills: cloneRequestedSkills(requestedSkills),
      ...(scope ? { composerScope: scope } : {}),
    })
    if (!trimmed && !attachments?.length && requestedSkills.length === 0) {
      return { ok: false, stage: 'startup' }
    }

    if (requestedSkills.length > 0 && deps.isWebSlashInput(trimmed)) {
      const message = deps.commandErrorMessage('slash.skill_syntax_invalid')
      deps.showCommandError('slash.skill_syntax_invalid', message, commandScope)
      return refused(message)
    }

    if (
      deps.isWebSlashInput(trimmed)
      && attachments?.length
      && (!isExternalAgent || deps.quickActionIDForSlash(trimmed) !== '')
    ) {
      const message = deps.commandErrorMessage('slash.attachments_unsupported')
      deps.showCommandError('slash.attachments_unsupported', message, commandScope)
      return refused(message)
    }

    // Command handling is the only await allowed before the optimistic turn
    // is appended, and only slash input can be a command. Plain text must not
    // yield at all: the send has to paint in the same frame as the Enter key.
    if (deps.isWebSlashInput(trimmed)) {
      const newCommand = await deps.handleWebNewCommand(trimmed, attachments, viewTarget)
      if (newCommand.kind === 'handled') return { ok: true }
      if (newCommand.kind === 'error') {
        return refused(newCommand.message)
      }
      const slashCommand = await deps.handleWebSlashCommand(
        trimmed,
        requestedSkills.length > 0,
        composerScope,
        viewTarget,
      )
      if (slashCommand.kind === 'handled') return { ok: true }
      if (slashCommand.kind === 'error') {
        return refused(slashCommand.message)
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
    let firstSendStarted = false

    const wasDraft = !viewTarget.sessionId
    const serverSlashActivation = deps.isWebSlashInput(trimmed)
      && deps.quickActionIDForSlash(trimmed) === ''
      && !isExternalAgent
    const serverSkillActivation = requestedSkills.length > 0 || serverSlashActivation
    if (serverSkillActivation && wasDraft && deps.pendingExternalAgentStateFor(viewTarget)) {
      const message = deps.commandErrorMessage('slash.skill_activation_unsupported')
      deps.showCommandError('slash.skill_activation_unsupported', message, commandScope)
      return refused(message, composerScope)
    }

    // A draft's first message creates its session in-band: the server creates
    // the session for a message without session_id and names it with
    // session_created before run_accepted. The turns are held until
    // run_accepted (see first-send.ts), so the draft only becomes a chat once
    // the server has taken the send. External Agent sessions need runtime
    // setup the message path does not do, so they still create over REST
    // first, and that creation is their confirmation.
    const inband = wasDraft && !isExternalAgent
    let stopConfirmationWatch = () => {}
    try {
      options.onBeforeMessageSend?.()
      // The pair comes from options only (spec v2 §3.4): the composer passes
      // it when the pair has an explicit source (user/session) and omits it
      // for default-sourced pairs, which is how the server tells "never
      // picked" apart from "picked the default".
      const modelId = options.modelId?.trim() || undefined
      const reasoningEffort = options.reasoningEffort?.trim()
        || undefined
      if (!inband) {
        viewTarget = await deps.ensureChatViewSession(viewTarget, wasDraft ? trimmed : undefined)
      }

      const botId = viewTarget.botId
      const targetSessionId = viewTarget.sessionId ?? ''
      if (!targetSessionId && !inband) throw new Error('Session not selected')
      sendBotId = botId
      sendSessionId = targetSessionId
      sendInvocationId = createInvocationId()
      const transcript = deps.transcriptForTarget(viewTarget)
      // A session this send just created over REST holds nothing yet but the
      // turn appended below; its first history load must not mask that turn.
      if (wasDraft && targetSessionId) deps.chatView(viewTarget).clientBorn = true
      if (targetSessionId) {
        deps.startSessionRuntime(botId, targetSessionId)
        deps.recordUserSent(viewTarget, targetSessionId, wasDraft)
      }
      // The workdir the draft is bound to travels with the message, because
      // the server now creates the session. The binding is fixed at creation.
      const workdirId = inband ? deps.draftWorkdirIdFor(botId).trim() : ''
      const replyTurn = transcript.createOptimisticAssistantTurn(sendInvocationId)
      assistantTurn = replyTurn
      const appendTurns = () => {
        turnAppendStarted = true
        options.onBeforeTurnAppend?.({ ...viewTarget })
        if (!serverSkillActivation) {
          userTurn = transcript.createOptimisticUserTurn(
            trimmed,
            attachments,
            sendInvocationId,
          )
          transcript.appendToView(userTurn, replyTurn)
        }
      }
      if (wasDraft) {
        // A REST-created session is already named, so that send starts
        // admitted and is revealed at once; an in-band one waits for
        // run_accepted, which reveals it before promoting the draft.
        deps.firstSend.begin(viewTarget, sendInvocationId, workdirId, appendTurns)
        firstSendStarted = true
        if (!inband) {
          deps.firstSend.admit(sendInvocationId, targetSessionId)
          deps.firstSend.reveal(sendInvocationId)
        }
      } else {
        appendTurns()
      }

      if (!deps.ensureWebSocket(botId)) {
        throw new StreamFailureError('WebSocket is not connected', 'startup')
      }
      const completion = deps.trackAssistantStream({
        onModelPreferenceSettled: options.onModelPreferenceSettled,
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
        workdir_id: workdirId || undefined,
        attachments,
        requested_skills: requestedSkills.length
          ? requestedSkillRequestsForWire(requestedSkills)
          : undefined,
        model_id: modelId,
        reasoning_effort: reasoningEffort,
        workspace_target_id: options.workspaceTargetId?.trim() || undefined,
      })) throw new StreamFailureError('WebSocket is not connected', 'startup')
      if (inband) stopConfirmationWatch = deps.watchFirstSendConfirmation(sendInvocationId)
      await completion
      if (firstSendStarted) deps.firstSend.finish(sendInvocationId)
      const createdSessionId = deps.createdSessionIdForInvocation(sendInvocationId)
      const fallbackActiveSessionId = !options.target
        && (deps.currentBotId.value ?? '').trim() === botId
        ? deps.sessionId.value ?? ''
        : ''
      const refreshSessionId = sendSessionId || createdSessionId || fallbackActiveSessionId
      deps.forgetCreatedSession(sendInvocationId)
      if (refreshSessionId) await deps.refreshCurrentSession(botId, refreshSessionId)

      return { ok: true, messageSent: true }
    } catch (error) {
      const failure = error instanceof Error ? error : new Error('Unknown error')
      const isAbort = failure.name === 'AbortError'
      const isCommandError = failure instanceof CommandStreamError
      const reason = resolveApiErrorMessage(error, failure.message || deps.sendFailedMessage())
      const errorCode = parseMemohError(error)?.code
      // A first send the server never confirmed showed nothing: the pane is
      // still on welcome with the input in the composer. Once revealed it is an
      // ordinary session, and its failure stays in the history.
      const held = firstSendStarted && !deps.firstSend.isRevealed(sendInvocationId)
      const revealedFirstSend = firstSendStarted && !held
      const reportedStage: SendMessageStage = failure instanceof StreamFailureError
        ? failure.stage
        : (assistantTurn ? failureStage(assistantTurn, false, false) : 'startup')
      const stage: SendMessageStage = held
        ? 'startup'
        : (revealedFirstSend ? 'stream' : reportedStage)
      const createdSessionId = sendInvocationId
        ? deps.createdSessionIdForInvocation(sendInvocationId)
        : ''
      const botId = sendBotId || viewTarget.botId || deps.currentBotId.value || ''
      const targetSessionId = sendSessionId || createdSessionId

      if (held) {
        // The server created a session but refused the run. Nothing shows it
        // yet, so it is deleted quietly. A send that ended without an answer
        // (the socket dropped, or no confirmation came) may have had its run
        // taken: deleting would hide that run, and the input handed back
        // could start it twice. That session is kept and becomes ordinary.
        const outcomeUnknown = failure instanceof StreamFailureError && failure.outcomeUnknown
        if (targetSessionId && !outcomeUnknown) {
          void deps.cleanupFailedDeferredSession(botId, targetSessionId, composerScope)
        }
        deps.firstSend.finish(sendInvocationId)
      } else {
        if (firstSendStarted) deps.firstSend.finish(sendInvocationId)
        if (assistantTurn) {
          deps.finalizeStreamFailure(assistantTurn, botId, targetSessionId, failure, !isAbort && stage === 'stream')
        }
        if (!isAbort && stage === 'startup' && userTurn) {
          deps.removeTurnFromSession(botId, targetSessionId, userTurn)
        }
      }

      if (sendInvocationId) deps.discardAssistantStream(sendInvocationId)
      if (sendInvocationId) deps.forgetCreatedSession(sendInvocationId)
      if (stage === 'startup' && turnAppendStarted && !isAbort) {
        options.onTurnAppendAborted?.()
      }

      if (isAbort && !held) return { ok: false, stage: 'stream', error: reason, errorCode }
      if (stage === 'startup') {
        const currentBotId = (deps.currentBotId.value ?? '').trim()
        const currentSessionId = (deps.sessionId.value ?? '').trim()
        // A held first send is restored to its draft composer, which the
        // failure is keyed to; the pane applies it only while it still shows
        // that draft. A failure on an existing session is restored only while
        // that session is still the active one. Command errors there are
        // already on screen in the command panel.
        const restorable = held
          ? currentBotId === botId
          : currentBotId === botId
            && !isCommandError
            && (!targetSessionId || currentSessionId === targetSessionId)
        if (options.restoreDraftOnFailure !== false && restorable) {
          deps.rememberStartupSendFailure({
            botId,
            sessionId: held ? '' : targetSessionId,
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
    } finally {
      stopConfirmationWatch()
    }
  }

  async function retryLatestAssistant(
    turnId: string,
    options: {
      target?: ChatViewTarget
      modelId?: string
      reasoningEffort?: string
      workspaceTargetId?: string
      /** See SendMessageOptions.onModelPreferenceSettled. */
      onModelPreferenceSettled?: () => void
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
        onModelPreferenceSettled: options.onModelPreferenceSettled,
        invocationId,
        assistantTurn,
        replacesTurn: true,
        botId,
        sessionId: targetSessionId,
      })
      if (!deps.sendWebSocketMessage(botId, {
        type: 'retry_message',
        invocation_id: invocationId,
        session_id: targetSessionId,
        turn_id: targetTurnId,
        model_id: options.modelId?.trim() || undefined,
        reasoning_effort: options.reasoningEffort?.trim()
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
        : failureStage(assistantTurn, true, deps.hasVisibleAssistantBlocks(assistantTurn))
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
      /** See SendMessageOptions.onModelPreferenceSettled. */
      onModelPreferenceSettled?: () => void
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
        onModelPreferenceSettled: options.onModelPreferenceSettled,
        invocationId,
        assistantTurn,
        replacesTurn: true,
        botId,
        sessionId: targetSessionId,
      })
      if (!deps.sendWebSocketMessage(botId, {
        type: 'edit_message',
        invocation_id: invocationId,
        session_id: targetSessionId,
        turn_id: targetTurnId,
        text: trimmed,
        model_id: options.modelId?.trim() || undefined,
        reasoning_effort: options.reasoningEffort?.trim()
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
        : failureStage(assistantTurn, true, deps.hasVisibleAssistantBlocks(assistantTurn))
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
