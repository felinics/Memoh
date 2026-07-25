import { access } from 'node:fs/promises'
import type { ReadStream } from 'node:fs'
import type { Duplex } from 'node:stream'
import { fileURLToPath } from 'node:url'

import {
  Server,
  ServerCredentials,
  status,
  type handleBidiStreamingCall,
  type handleClientStreamingCall,
  type handleServerStreamingCall,
  type handleUnaryCall,
  type ServerUnaryCall,
  type ServiceDefinition,
  type UntypedServiceImplementation,
} from '@grpc/grpc-js'
import { loadSync } from '@grpc/proto-loader'

import { ChildSupervisor } from './children'
import { WorkspaceExecService } from './core/exec'
import { rawChunkSize, WorkspaceFileService } from './core/fs'
import { HostPathResolver } from './core/paths'
import { mapNodeError, rpcError } from './rpc'
import type {
  DeleteFileRequest,
  ExecInput,
  ExecOutput,
  ListDirRequest,
  MkdirRequest,
  ReadFileRequest,
  ReadRawRequest,
  RenameRequest,
  StatRequest,
  WriteFileRequest,
  WriteRawChunk,
} from './types'

export const grpcMessageLimit = 16 * 1024 * 1024

export interface RuntimeGrpcServerOptions {
  workspaceBase: string
  warn?: (message: string) => void
}

export interface RunningRuntimeGrpcServer {
  acceptConnection(connection: Duplex): void
  close(): Promise<void>
}

interface ContainerServiceConstructor {
  service: ServiceDefinition
}

let serviceDefinitionPromise: Promise<ServiceDefinition> | undefined

export async function startRuntimeGrpcServer(
  options: RuntimeGrpcServerOptions,
): Promise<RunningRuntimeGrpcServer> {
  const paths = await HostPathResolver.create(options.workspaceBase)
  const children = new ChildSupervisor({ warn: options.warn })
  let acceptingRPCs = true
  const implementation = createContainerService(paths, children, () => acceptingRPCs)
  const server = new Server({
    'grpc.max_receive_message_length': grpcMessageLimit,
    'grpc.max_send_message_length': grpcMessageLimit,
  })
  server.addService(await loadContainerServiceDefinition(), implementation)
  // grpc-js 1.14.4 exposes a public connection injector that hands an
  // existing Duplex directly to its HTTP/2 server. This deliberately avoids
  // opening a second loopback TCP, Unix-socket, or named-pipe listener.
  const injector = server.createConnectionInjector(ServerCredentials.createInsecure())
  const connections = new Set<Duplex>()
  let closing = false
  let closePromise: Promise<void> | undefined
  return {
    acceptConnection(connection) {
      if (closing || connection.destroyed) {
        connection.destroy()
        throw new Error('runtime gRPC server is closing')
      }
      connections.add(connection)
      connection.once('close', () => connections.delete(connection))
      try {
        injector.injectConnection(connection)
      } catch (error) {
        connections.delete(connection)
        connection.destroy()
        throw error
      }
    },
    close() {
      closePromise ??= (async () => {
        closing = true
        acceptingRPCs = false
        // The injected Duplex and every process started through it belong to
        // this one connection. Tear both down before accepting a reconnect.
        for (const connection of connections) {
          connection.destroy()
        }
        connections.clear()
        await children.close()
        // tryShutdown closes the injector-owned HTTP/2 server and releases
        // the channelz reference created by createConnectionInjector().
        await new Promise<void>(resolve => {
          const timer = setTimeout(() => {
            server.forceShutdown()
            resolve()
          }, 2_000)
          timer.unref()
          server.tryShutdown(() => {
            clearTimeout(timer)
            resolve()
          })
        })
      })()
      return closePromise
    },
  }
}

export async function loadContainerServiceDefinition(): Promise<ServiceDefinition> {
  serviceDefinitionPromise ??= loadContainerServiceDefinitionUncached()
  return serviceDefinitionPromise
}

async function loadContainerServiceDefinitionUncached(): Promise<ServiceDefinition> {
  const protoPath = await locateBridgeProto()
  const definition = loadSync(protoPath, {
    keepCase: true,
    longs: String,
    enums: Number,
    defaults: true,
    oneofs: true,
  })
  const descriptor = (await import('@grpc/grpc-js')).loadPackageDefinition(definition) as unknown as {
    bridgepb: { ContainerService: ContainerServiceConstructor }
  }
  return descriptor.bridgepb.ContainerService.service
}

export function createContainerService(
  paths: HostPathResolver,
  children: ChildSupervisor,
  acceptingRPCs: () => boolean = () => true,
): UntypedServiceImplementation {
  const files = new WorkspaceFileService(paths)
  const commands = new WorkspaceExecService(paths, children, acceptingRPCs)

  const ReadFile: handleUnaryCall<ReadFileRequest, unknown> = unary(async call => files.readFile(call.request))
  const WriteFile: handleUnaryCall<WriteFileRequest, unknown> = unary(async call => files.writeFile(call.request))
  const ListDir: handleUnaryCall<ListDirRequest, unknown> = unary(async call => files.listDir(call.request))
  const Stat: handleUnaryCall<StatRequest, unknown> = unary(async call => files.stat(call.request))
  const Mkdir: handleUnaryCall<MkdirRequest, unknown> = unary(async call => files.mkdir(call.request))
  const Rename: handleUnaryCall<RenameRequest, unknown> = unary(async call => files.rename(call.request))
  const DeleteFile: handleUnaryCall<DeleteFileRequest, unknown> = unary(async call => files.deleteFile(call.request))

  const Exec: handleBidiStreamingCall<ExecInput, ExecOutput> = call => {
    commands.exec(call)
  }
  const Tunnel: handleBidiStreamingCall<unknown, unknown> = call => {
    const error = rpcError(status.PERMISSION_DENIED, 'tunnels are not allowed by Remote Runtime M1')
    call.emit('error', error)
  }
  const ReverseHTTP: handleBidiStreamingCall<unknown, unknown> = call => {
    const error = rpcError(status.UNAVAILABLE, 'ReverseHTTP is not available in Remote Runtime M1')
    call.emit('error', error)
  }

  const ReadRaw: handleServerStreamingCall<ReadRawRequest, { data: Buffer }> = call => {
    let cancelled = call.cancelled || call.destroyed
    let input: ReadStream | undefined
    const cancel = () => {
      cancelled = true
      input?.destroy()
    }
    call.once('cancelled', cancel)
    void (async () => {
      if (!call.request.path?.trim()) {
        throw rpcError(status.INVALID_ARGUMENT, 'path is required')
      }
      if (cancelled || call.destroyed) {
        return
      }
      let handle: Awaited<ReturnType<WorkspaceFileService['openReadRaw']>> | undefined
      try {
        handle = await files.openReadRaw(call.request.path)
        if (cancelled || call.destroyed) {
          return
        }
        input = handle.createReadStream({ highWaterMark: rawChunkSize, autoClose: true })
        handle = undefined
        for await (const chunk of input) {
          if (cancelled || call.destroyed) {
            return
          }
          const response = { data: Buffer.from(chunk) }
          if (!call.write(response)) {
            await waitForDrainOrCancellation(call)
          }
        }
        if (!cancelled && !call.destroyed) {
          call.end()
        }
      } finally {
        await handle?.close().catch(() => undefined)
      }
    })().catch(error => {
      if (!call.cancelled && !call.destroyed) {
        const mapped = mapNodeError(error, 'read raw')
        call.emit('error', mapped)
      }
    }).finally(() => call.off('cancelled', cancel))
  }

  const WriteRaw: handleClientStreamingCall<WriteRawChunk, { bytes_written: string }> = (call, callback) => {
    let handle: Awaited<ReturnType<WorkspaceFileService['openWriteRaw']>> | undefined
    let written = 0
    let chain = Promise.resolve()
    let settled = call.cancelled || call.destroyed
    let cancelled = settled
    const fail = async (error: unknown) => {
      if (settled) {
        return
      }
      settled = true
      await handle?.close().catch(() => undefined)
      if (!call.cancelled && !call.destroyed) {
        const mapped = mapNodeError(error, 'write raw')
        callback(mapped)
      }
    }
    call.on('data', (chunk: WriteRawChunk) => {
      if (settled || cancelled) {
        return
      }
      call.pause()
      chain = chain.then(async () => {
        if (settled || cancelled) {
          return
        }
        if (!handle) {
          if (!chunk.path?.trim()) {
            throw rpcError(status.INVALID_ARGUMENT, 'first chunk must include path')
          }
          const opened = await files.openWriteRaw(chunk.path)
          if (settled || cancelled || call.destroyed) {
            await opened.close().catch(() => undefined)
            return
          }
          handle = opened
        }
        if (!settled && !cancelled && chunk.data?.length) {
          const result = await handle.write(Buffer.from(chunk.data))
          written += result.bytesWritten
        }
      })
      void chain.then(() => {
        if (!settled) {
          call.resume()
        }
      }, fail)
    })
    call.on('end', () => {
      void chain.then(async () => {
        if (settled || cancelled || call.destroyed) {
          return
        }
        settled = true
        await handle?.close()
        const response = { bytes_written: String(written) }
        callback(null, response)
      }).catch(fail)
    })
    call.on('cancelled', () => {
      if (settled) {
        return
      }
      cancelled = true
      settled = true
      void handle?.close().catch(() => undefined)
    })
    call.on('error', error => void fail(error))
  }

  return {
    ReadFile,
    WriteFile,
    ListDir,
    Stat,
    Mkdir,
    Rename,
    Exec,
    Tunnel,
    ReverseHTTP,
    ReadRaw,
    WriteRaw,
    DeleteFile,
  }
}

function waitForDrainOrCancellation(call: {
  cancelled?: boolean
  once(event: string, listener: () => void): unknown
  off(event: string, listener: () => void): unknown
}): Promise<void> {
  if (call.cancelled) {
    return Promise.resolve()
  }
  return new Promise(resolve => {
    let settled = false
    const finish = () => {
      if (!settled) {
        settled = true
        call.off('drain', onDrain)
        call.off('cancelled', onCancelled)
        resolve()
      }
    }
    const onDrain = () => finish()
    const onCancelled = () => finish()
    call.once('drain', onDrain)
    call.once('cancelled', onCancelled)
  })
}

function unary<Request, Response>(
  operation: (call: ServerUnaryCall<Request, Response>) => Promise<Response>,
): handleUnaryCall<Request, Response> {
  return (call, callback) => {
    void operation(call)
      .then(response => {
        callback(null, response)
      })
      .catch(error => {
        const mapped = mapNodeError(error, 'request')
        callback(mapped)
      })
  }
}

async function locateBridgeProto(): Promise<string> {
  const candidates = [
    fileURLToPath(new URL('./bridge.proto', import.meta.url)),
    fileURLToPath(new URL('../../../domains/runtime/bridge/pb/bridge.proto', import.meta.url)),
  ]
  for (const candidate of candidates) {
    try {
      await access(candidate)
      return candidate
    } catch {
      // Try the source-tree path, then the packaged build asset.
    }
  }
  throw new Error('canonical bridge.proto was not found')
}
