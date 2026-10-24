import type { RuntimeCommand } from '@/utils/runtime-slash-commands'
import { parseSessionQueueCommand } from './session-queue-submission'

export interface ComposerActionInput {
  text: string
  hasFiles: boolean
  hasSkills: boolean
  streaming: boolean
  awaitingFirstSend: boolean
  queuePending: boolean
  active: boolean
  hasBot: boolean
  hasSession: boolean
  readOnly: boolean
  loadingMessages: boolean
  configPending: 'agent' | 'mode' | 'model' | false
  hasNoModel: boolean
  modeUnavailable: boolean
  modeChangeDisabled: boolean
  agentUnavailable: boolean
  localCommand: string
  runtimeCommands: readonly RuntimeCommand[]
  runtimeCommandsUnresolved: boolean
  goalDraft: boolean
  goalBlocked: boolean
}

type Intent =
  | { kind: 'idle' }
  | { kind: 'waiting' }
  | { kind: 'stop' }
  | { kind: 'send'; text: string; goal: boolean }
  | { kind: 'enqueue'; text: string; mode: 'steer' | 'follow-up' }
  | { kind: 'local-command'; text: string; id: string }
  | { kind: 'runtime-command'; command: RuntimeCommand }

// Stable local reasons; translated by the host, never matched against messages.
export type ComposerBlockReason =
  | 'inactive' | 'agent-unavailable'
  | 'common.loading' | 'chat.readonlyHint' | 'chat.composer.selectBot'
  | 'chat.composer.selectModel' | 'chat.composer.modeChanging'
  | 'chat.runtimeModeModelUnavailable' | 'chat.queue.textOnly'
  | 'chat.slash.attachmentsUnsupported' | 'errors.slash.skill_syntax_invalid'
  | 'errors.queue_request_invalid' | 'errors.session_runtime.session_busy'
  | 'errors.runtime_control.goal_requires_default_mode' | 'chat.goal.textOnly'

export type ComposerAction = Intent & { blockedReason?: ComposerBlockReason }
export type ComposerActionSource = 'button' | 'submit'

/** Resolve intent before availability: an invalid draft must never become Stop. */
export function resolveComposerAction(input: ComposerActionInput): ComposerAction {
  const text = input.text.trim()
  const hasContent = !!text || input.hasFiles || input.hasSkills
  const queue = parseSessionQueueCommand(text, input.runtimeCommands)
  const selector = text.startsWith('/') ? text.slice(1).split(/\s/, 1)[0] : ''
  const runtimeCommand = input.runtimeCommands.find(command => command.name === selector)
  const goal = input.goalDraft && !text.startsWith('/')
  let intent: Intent
  if (input.awaitingFirstSend) intent = { kind: 'waiting' }
  else if (!hasContent) intent = { kind: input.streaming ? 'stop' : 'idle' }
  else if (input.localCommand) intent = { kind: 'local-command', id: input.localCommand, text }
  else if (runtimeCommand && runtimeCommand.kind !== 'turn') intent = { kind: 'runtime-command', command: runtimeCommand }
  else if (input.streaming || queue) intent = { kind: 'enqueue', mode: queue?.mode ?? 'follow-up', text: queue?.text ?? text }
  else intent = { kind: 'send', text, goal }

  const blockedReason = availability()
  return { ...intent, ...(blockedReason ? { blockedReason } : {}) }

  function availability(): ComposerBlockReason | undefined {
    if (!input.active) return 'inactive'
    if (!input.hasBot) return 'chat.composer.selectBot'
    if (input.readOnly) return 'chat.readonlyHint'
    // Stopping must remain possible while model/configuration controls reload.
    if (intent.kind === 'stop' || intent.kind === 'idle' || intent.kind === 'waiting') return
    if (input.loadingMessages) return 'common.loading'
    if (intent.kind === 'enqueue') {
      if (input.hasFiles || input.hasSkills) return 'chat.queue.textOnly'
      if (!input.hasSession || !intent.text) return 'errors.queue_request_invalid'
      if (runtimeCommand || (input.runtimeCommandsUnresolved && text.startsWith('/') && !queue) || goal) return 'errors.session_runtime.session_busy'
      if (input.queuePending) return 'common.loading'
      return
    }
    if (input.goalBlocked && (goal || /^\/goal(?:\s|$)/i.test(text))) return 'errors.runtime_control.goal_requires_default_mode'
    if (intent.kind === 'local-command' || intent.kind === 'runtime-command') {
      if (input.hasFiles) return 'chat.slash.attachmentsUnsupported'
      if (input.hasSkills) return 'errors.slash.skill_syntax_invalid'
      if (intent.kind === 'runtime-command') {
        if (input.streaming && intent.command.kind === 'operation') return 'errors.session_runtime.session_busy'
      } else if ((intent.id === 'goal' || intent.id === 'plan') && input.modeChangeDisabled) {
        return input.streaming ? 'errors.session_runtime.session_busy' : 'common.loading'
      }
      return
    }
    if ((goal || /^\/goal(?:\s|$)/i.test(text)) && (input.hasFiles || input.hasSkills)) return 'chat.goal.textOnly'
    if (input.configPending) return input.configPending === 'mode' ? 'chat.composer.modeChanging' : 'common.loading'
    if (input.hasNoModel) return 'chat.composer.selectModel'
    if (input.modeUnavailable) return 'chat.runtimeModeModelUnavailable'
    if (input.agentUnavailable && !/^\/new(?:\s|$)/i.test(text)) return 'agent-unavailable'
  }
}

export function composerActionPresentation(action: ComposerAction) {
  return {
    visible: action.kind !== 'idle',
    disabled: !!action.blockedReason || action.kind === 'waiting' || action.kind === 'idle',
    busy: action.kind === 'waiting',
    icon: action.kind === 'stop' ? 'stop' : action.kind === 'waiting' ? 'busy' : 'send',
    labelKey: action.kind === 'stop' ? 'chat.stopResponse'
      : action.kind === 'waiting' ? 'common.loading'
        : action.kind === 'enqueue' ? (action.mode === 'steer' ? 'chat.queue.enqueueSteer' : 'chat.queue.enqueueFollowUp')
          : 'chat.send',
    preserveOpacity: action.blockedReason === 'chat.composer.modeChanging',
  }
}

type ExecutableKind = Exclude<ComposerAction['kind'], 'idle' | 'waiting'>
type ComposerActionHandlers = {
  [K in ExecutableKind]: (action: Extract<ComposerAction, { kind: K }>) => void | Promise<void>
}

/** Keyboard/command submission shares dispatch with clicks, but can never stop. */
export async function dispatchComposerAction(action: ComposerAction, source: ComposerActionSource, handlers: ComposerActionHandlers): Promise<void> {
  if (composerActionPresentation(action).disabled) return
  switch (action.kind) {
    case 'stop': if (source === 'button') await handlers.stop(action); return
    case 'send': await handlers.send(action); return
    case 'enqueue': await handlers.enqueue(action); return
    case 'local-command': await handlers['local-command'](action); return
    case 'runtime-command': await handlers['runtime-command'](action); return
  }
}
