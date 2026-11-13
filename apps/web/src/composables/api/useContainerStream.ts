import { client } from '@memohai/sdk/client'
import type { Options } from '@memohai/sdk'
import type { PostBotsByBotIdContainerData } from '@memohai/sdk'
import {
  isContainerCreateStreamEvent,
  type ContainerCreateLayerStatus,
  type ContainerCreateStreamEvent,
} from './container-stream-events'
import { openJsonSseStream } from './stream-json-sse'

export type { ContainerCreateLayerStatus, ContainerCreateStreamEvent }

export type ContainerCreateStreamResult = {
  stream: AsyncGenerator<ContainerCreateStreamEvent, void, unknown>
}

export async function postBotsByBotIdContainerStream(
  options: Options<PostBotsByBotIdContainerData>,
): Promise<ContainerCreateStreamResult> {
  const { throwOnError: _throwOnError, ...rest } = options
  return openJsonSseStream<ContainerCreateStreamEvent>({
    signal: options.signal,
    invalidEventMessage: 'Invalid workspace creation stream event',
    failureMessage: 'Workspace creation stream failed',
    isEvent: isContainerCreateStreamEvent,
    open: request =>
      client.sse.post<ContainerCreateStreamEvent>({
        url: '/bots/{bot_id}/container',
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
