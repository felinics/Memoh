import { client } from '@memohai/sdk/client'
import type { Options } from '@memohai/sdk'
import type { BotsBot, HandlersCreateContainerResponse, PostBotsData } from '@memohai/sdk'
import {
  containerLayerProgressPercent,
  isContainerCreateStreamEvent,
  type ContainerCreateLayerStatus,
} from './container-stream-events'
import type { SSEErrorEvent } from './sse-error'
import { openJsonSseStream } from './stream-json-sse'

// codesync(bot-create-stream): keep these manual SSE payload types in sync
// with internal/handlers/users.go.
export type BotCreateStreamEvent =
  | { type: 'bot_created'; bot: BotsBot }
  | { type: 'pulling'; image: string }
  | { type: 'pull_progress'; layers: ContainerCreateLayerStatus[] }
  | { type: 'pull_skipped'; image: string; message?: string }
  | { type: 'pull_delegated'; image: string; message?: string }
  | { type: 'creating' }
  | { type: 'restoring' }
  | { type: 'complete'; container: HandlersCreateContainerResponse }
  | { type: 'ready'; bot: BotsBot }
  | SSEErrorEvent

export type BotCreateStreamResult = {
  stream: AsyncGenerator<BotCreateStreamEvent, void, unknown>
}

export type BotCreateProgress = {
  phase: 'preserving' | 'pulling' | 'creating' | 'restoring' | 'complete' | 'error'
  layers?: ContainerCreateLayerStatus[]
  image?: string
  error?: string
}

export type BotCreateProgressState = {
  bot?: BotsBot
  progress?: BotCreateProgress
  setupError?: string
  errorCode?: string
}

export function botCreateProgressPercent(progress: BotCreateProgress | null | undefined): number {
  return containerLayerProgressPercent(progress?.layers)
}

export function reduceBotCreateProgressEvent(
  state: BotCreateProgressState,
  event: BotCreateStreamEvent,
): BotCreateProgressState {
  switch (event.type) {
    case 'bot_created':
      return { ...state, bot: event.bot }
    case 'pulling':
      return { ...state, progress: { phase: 'pulling', image: event.image } }
    case 'pull_progress':
      return {
        ...state,
        progress: {
          phase: 'pulling',
          image: state.progress?.image,
          layers: event.layers,
        },
      }
    case 'pull_skipped':
    case 'pull_delegated':
      return {
        ...state,
        progress: event.image === 'local'
          ? { phase: 'creating' }
          : { phase: 'pulling', image: event.image },
      }
    case 'creating':
      return { ...state, progress: { phase: 'creating' } }
    case 'restoring':
      return { ...state, progress: { phase: 'restoring' } }
    case 'complete':
      return { ...state, progress: { phase: 'complete' } }
    case 'ready':
      return { ...state, bot: event.bot }
    case 'error':
      return {
        ...state,
        setupError: event.message,
        errorCode: event.code,
        progress: { phase: 'error', error: event.message },
      }
  }
}

export type CollectBotCreateProgressStreamOptions = {
  initialState?: BotCreateProgressState
  onState?: (state: BotCreateProgressState) => void
  onEvent?: (event: BotCreateStreamEvent) => void
}

export async function collectBotCreateProgressStream(
  stream: AsyncGenerator<BotCreateStreamEvent, void, unknown>,
  options: CollectBotCreateProgressStreamOptions = {},
): Promise<BotCreateProgressState> {
  let state = options.initialState ?? {}
  let ready = false

  const update = (next: BotCreateProgressState) => {
    state = next
    options.onState?.(state)
  }

  try {
    for await (const event of stream) {
      options.onEvent?.(event)
      if (event.type === 'ready') {
        ready = true
      }
      update(reduceBotCreateProgressEvent(state, event))
    }
  } catch (error) {
    if (!state.bot) {
      throw error
    }
    if (ready) {
      return state
    }
    const message = toError(error).message
    update({
      ...state,
      setupError: message,
      progress: { phase: 'error', error: message },
    })
  }

  return state
}

function isBot(value: unknown): value is BotsBot {
  return !!value && typeof value === 'object'
}

function isBotCreateStreamEvent(value: unknown): value is BotCreateStreamEvent {
  if (!value || typeof value !== 'object') return false

  const event = value as Record<string, unknown>
  if (event.type === 'bot_created' || event.type === 'ready') {
    return isBot(event.bot)
  }
  return isContainerCreateStreamEvent(value)
}

function toError(error: unknown): Error {
  if (error instanceof Error) return error
  if (typeof error === 'string' && error.trim()) return new Error(error)
  return new Error('Bot create stream failed')
}

export async function postBotsStream(
  options: Options<PostBotsData>,
): Promise<BotCreateStreamResult> {
  const { throwOnError: _throwOnError, ...rest } = options
  return openJsonSseStream<BotCreateStreamEvent>({
    signal: options.signal,
    invalidEventMessage: 'Invalid bot create stream event',
    failureMessage: 'Bot create stream failed',
    isEvent: isBotCreateStreamEvent,
    open: request =>
      client.sse.post<BotCreateStreamEvent>({
        url: '/bots',
        ...rest,
        ...request,
        headers: {
          ...options.headers as Record<string, string>,
          ...request.headers,
          'Content-Type': 'application/json',
        },
      }),
  })
}
