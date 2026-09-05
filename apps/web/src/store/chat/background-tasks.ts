import { reactive } from 'vue'
import type { UIBackgroundTask } from '@/composables/api/useChat'
import { asRecord, pickRawString, pickString, taskIdFromToolBlock } from '../chat-list.normalize'
import type { BackgroundTask, ChatMessage, ToolCallBlock } from './types'

export type { BackgroundTask } from './types'

// Background-task tracking — normalizes the loosely-shaped background_task
// payloads that arrive over WS/SSE, remembers the latest state per task id,
// and folds that state into the tool blocks that spawned the tasks. Pure
// merge/normalize logic plus one Map; the chat store instantiates a tracker
// and passes its own `messages` array where a scan is needed, so this module
// never holds transcript state itself.

export function normalizeBackgroundStatus(status?: string, event?: string): string {
  const token = (status || event || '').trim().toLowerCase()
  switch (token) {
    case 'background_started':
    case 'auto_backgrounded':
    case 'started':
    case 'output':
    case 'running':
      return 'running'
    case 'queued':
    case 'queue':
      return 'queued'
    case 'complete':
    case 'completed':
    case 'success':
    case 'succeeded':
      return 'completed'
    case 'error':
    case 'failed':
    case 'failure':
      return 'failed'
    case 'stalled':
      return 'stalled'
    case 'killed':
    case 'cancelled':
    case 'canceled':
      return 'killed'
    case 'unknown':
      return 'unknown'
    default:
      return ''
  }
}

export function isBackgroundTaskActive(task?: BackgroundTask): boolean {
  const status = normalizeBackgroundStatus(task?.status, task?.event)
  return status === 'running' || status === 'queued' || status === 'stalled'
}

export function normalizeBackgroundTask(task?: UIBackgroundTask, eventType?: string): BackgroundTask | null {
  if (!task) return null
  const outer = task as Record<string, unknown>
  const nested = asRecord(outer.task)
  const record = Object.keys(nested).length > 0 ? nested : outer
  const taskId = pickString(record, 'task_id', 'taskId')
  if (!taskId) return null
  const event = pickString(record, 'event') || pickString(outer, 'event') || (eventType ?? '').trim()
  const status = normalizeBackgroundStatus(pickString(record, 'status'), event) || 'running'
  const exitCode = record.exit_code ?? record.exitCode
  return {
    taskId,
    status,
    event: event || undefined,
    botId: pickString(record, 'bot_id', 'botId') || pickString(outer, 'bot_id', 'botId') || undefined,
    sessionId: pickString(record, 'session_id', 'sessionId') || pickString(outer, 'session_id', 'sessionId') || undefined,
    command: pickString(record, 'command') || undefined,
    agentId: pickString(record, 'agent_id', 'agentId') || undefined,
    agentSessionId: pickString(record, 'agent_session_id', 'agentSessionId') || undefined,
    outputFile: pickString(record, 'output_file', 'outputFile') || undefined,
    outputTail: pickRawString(record, 'output_tail', 'outputTail', 'tail') || undefined,
    stream: pickString(record, 'stream') || undefined,
    chunk: pickRawString(record, 'chunk') || undefined,
    exitCode: typeof exitCode === 'number' ? exitCode : undefined,
    duration: pickString(record, 'duration') || undefined,
    stalled: record.stalled === true || status === 'stalled',
  }
}

export function mergeBackgroundTask(existing: BackgroundTask | undefined, incoming: BackgroundTask): BackgroundTask {
  const merged: BackgroundTask = existing ? { ...existing } : { taskId: incoming.taskId, status: incoming.status }
  const writable = merged as unknown as Record<string, unknown>
  for (const [key, value] of Object.entries(incoming)) {
    if (value === undefined || value === '') continue
    writable[key] = value
  }
  if (!incoming.outputTail && incoming.chunk) {
    merged.outputTail = `${existing?.outputTail ?? ''}${incoming.chunk}`.slice(-4096)
  }
  merged.status = normalizeBackgroundStatus(merged.status, merged.event) || merged.status || 'running'
  merged.stalled = merged.stalled === true || merged.status === 'stalled'
  return merged
}

export function mergeBackgroundTaskIntoToolBlock(block: ToolCallBlock, task: BackgroundTask) {
  const merged = mergeBackgroundTask(block.backgroundTask, task)
  block.backgroundTask = merged
  block.done = !isBackgroundTaskActive(merged)
  block.running = !block.done
  block.background_task = {
    task_id: merged.taskId,
    status: merged.status,
    event: merged.event,
    bot_id: merged.botId,
    session_id: merged.sessionId,
    command: merged.command,
    output_file: merged.outputFile,
    output_tail: merged.outputTail,
    stream: merged.stream,
    chunk: merged.chunk,
    exit_code: merged.exitCode,
    duration: merged.duration,
    stalled: merged.stalled,
  }
}

export function reconcileBackgroundTasksInMessages(items: ChatMessage[]) {
  const toolsByTaskId = new Map<string, ToolCallBlock>()
  for (const item of items) {
    if (item.role === 'assistant') {
      for (const block of item.messages) {
        if (block.type !== 'tool') continue
        const taskId = taskIdFromToolBlock(block)
        if (taskId) toolsByTaskId.set(taskId, block)
      }
      continue
    }
    if (item.role === 'system' && item.kind === 'background_task') {
      const target = toolsByTaskId.get(item.backgroundTask.taskId)
      if (target) mergeBackgroundTaskIntoToolBlock(target, item.backgroundTask)
    }
  }
}

export function createBackgroundTaskTracker() {
  // Reactive so a renderer that looks a task up by id (the dependency-missing
  // feedback block) re-renders as later events arrive for it.
  const latestBackgroundTasks = reactive(new Map<string, BackgroundTask>())

  function rememberBackgroundTask(task: BackgroundTask): BackgroundTask {
    const latest = mergeBackgroundTask(latestBackgroundTasks.get(task.taskId), task)
    latestBackgroundTasks.set(task.taskId, latest)
    return latest
  }

  function applyPendingBackgroundEventsToTool(block: ToolCallBlock) {
    const taskId = taskIdFromToolBlock(block)
    if (!taskId) return
    const latest = latestBackgroundTasks.get(taskId)
    if (latest) {
      mergeBackgroundTaskIntoToolBlock(block, latest)
    }
  }

  // Scans the caller-owned transcript for tool blocks spawned by this task.
  // `messages` is passed in (not captured) so the tracker stays transcript-free.
  function mergeBackgroundTaskIntoMatchingTools(task: BackgroundTask, messages: ChatMessage[]) {
    for (const item of messages) {
      if (item.role !== 'assistant') continue
      for (const block of item.messages) {
        if (block.type !== 'tool') continue
        if (taskIdFromToolBlock(block) === task.taskId) {
          mergeBackgroundTaskIntoToolBlock(block, task)
        }
      }
    }
  }

  function clearBackgroundTasks() {
    latestBackgroundTasks.clear()
  }

  /** Latest known state of one task, or undefined when no event named it yet. */
  function backgroundTaskFor(taskId: string): BackgroundTask | undefined {
    const id = taskId.trim()
    return id ? latestBackgroundTasks.get(id) : undefined
  }

  return {
    rememberBackgroundTask,
    applyPendingBackgroundEventsToTool,
    mergeBackgroundTaskIntoMatchingTools,
    clearBackgroundTasks,
    backgroundTaskFor,
  }
}
