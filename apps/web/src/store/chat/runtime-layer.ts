import type { RuntimeProjectionChange } from './runtime-client'
import type { UIStreamEvent } from '@/composables/api/useChat'
import {
  createChatDecisions,
  type ChatDecisionDeps,
} from './decisions'
import {
  createChatRealtimeController,
  type ChatRealtimeCallbacks,
} from './realtime'
import {
  createRuntimeIntegration,
  type RuntimeIntegrationDeps,
} from './runtime-integration'

export interface ChatRuntimeLayerDeps
  extends Omit<RuntimeIntegrationDeps, 'decisions' | 'realtime'> {
  transcriptForTarget: ChatDecisionDeps['transcriptForTarget']
  connectionLostMessage: ChatDecisionDeps['connectionLostMessage']
  resolveErrorMessage: ChatDecisionDeps['resolveErrorMessage']
  showError: ChatDecisionDeps['showError']
  createControlId: ChatDecisionDeps['createControlId']
  onBotSessionsActivityEvent: ChatRealtimeCallbacks['onBotSessionsActivityEvent']
}

export function createChatRuntimeLayer(deps: ChatRuntimeLayerDeps) {
  let forwardWebSocketEvent: (botId: string, event: UIStreamEvent) => void =
    () => {}
  let forwardRuntimeProjection: (
    botId: string,
    sessionId: string,
    change: RuntimeProjectionChange,
  ) => void = () => {}
  let forwardPrepareSessionRuntime: (
    botId: string,
    sessionId: string,
    commitInitialHistory: (applyHistory: () => void) => Promise<void>,
  ) => Promise<void> = async () => {}

  const realtime = createChatRealtimeController({
    onWebSocketEvent: (botId, event) => forwardWebSocketEvent(botId, event),
    prepareSessionRuntime: (botId, sessionId, commitInitialHistory) =>
      forwardPrepareSessionRuntime(botId, sessionId, commitInitialHistory),
    onRuntimeProjection: (botId, sessionId, change) =>
      forwardRuntimeProjection(botId, sessionId, change),
    onBotSessionsActivityEvent: deps.onBotSessionsActivityEvent,
  })
  const decisions = createChatDecisions({
    normalizeTarget: target => deps.normalizeTarget(target),
    transcriptForTarget: deps.transcriptForTarget,
    currentRun: sessionId =>
      realtime.runtimeProjection(sessionId)?.currentRunView ?? null,
    ensureWebSocket: realtime.ensureWebSocket,
    send: realtime.sendWebSocketMessage,
    createControlId: deps.createControlId,
    connectionLostMessage: deps.connectionLostMessage,
    resolveErrorMessage: deps.resolveErrorMessage,
    showError: deps.showError,
  })
  const integration = createRuntimeIntegration({
    ...deps,
    decisions,
    realtime,
  })
  forwardWebSocketEvent = (botId, event) =>
    integration.handleWebSocketEvent(event, botId)
  forwardRuntimeProjection = integration.handleProjection
  forwardPrepareSessionRuntime = integration.prepareSessionRuntime

  return { realtime, decisions, integration }
}
