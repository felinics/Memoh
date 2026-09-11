import { botCreateProgressPercent, type BotCreateStreamEvent } from './useBotCreateStream'

// codesync(bot-create-stream): line kinds mirror the SSE event phases emitted by
// internal/handlers/users.go, rendered as a pseudo-terminal log on the client.
export type BotCreateTerminalLineStatus = 'running' | 'done' | 'error' | 'info'

export type BotCreateTerminalLineKind =
  | 'command'
  | 'bot-created'
  | 'pulling'
  | 'creating'
  | 'restoring'
  | 'ready'
  | 'applying-settings'
  | 'installing-agent'
  | 'error'

export type BotCreateTerminalLine = {
  id: string
  kind: BotCreateTerminalLineKind
  status: BotCreateTerminalLineStatus
  image?: string
  percent?: number
  message?: string
}

type NewBotCreateTerminalLine = Omit<BotCreateTerminalLine, 'id'>

function withId(line: NewBotCreateTerminalLine, index: number): BotCreateTerminalLine {
  return { ...line, id: `${line.kind}-${index}` }
}

// Close out the trailing line if it is still running; other lines are untouched.
export function finalizeBotCreateTerminalLines(
  lines: BotCreateTerminalLine[],
  status: 'done' | 'error' = 'done',
): BotCreateTerminalLine[] {
  if (lines.length === 0) return lines
  const last = lines.at(-1)
  if (!last || last.status !== 'running') return lines
  return [...lines.slice(0, -1), { ...last, status }]
}

// Append a fresh line, first finalizing any trailing running line to done so the
// log reads as a sequence of completed steps with a single active one.
export function pushBotCreateTerminalLine(
  lines: BotCreateTerminalLine[],
  line: NewBotCreateTerminalLine,
): BotCreateTerminalLine[] {
  const finalized = finalizeBotCreateTerminalLines(lines)
  return [...finalized, withId(line, finalized.length)]
}

export function appendBotCreateTerminalLine(
  lines: BotCreateTerminalLine[],
  event: BotCreateStreamEvent,
): BotCreateTerminalLine[] {
  switch (event.type) {
    case 'bot_created':
      return pushBotCreateTerminalLine(lines, { kind: 'bot-created', status: 'done' })
    case 'pulling':
      return pushBotCreateTerminalLine(lines, { kind: 'pulling', status: 'running', image: event.image })
    case 'pull_progress': {
      const percent = botCreateProgressPercent({ phase: 'pulling', layers: event.layers })
      const last = lines[lines.length - 1]
      if (last && last.kind === 'pulling' && last.status === 'running') {
        return [...lines.slice(0, -1), { ...last, percent }]
      }
      return pushBotCreateTerminalLine(lines, { kind: 'pulling', status: 'running', percent })
    }
    case 'pull_skipped':
    case 'pull_delegated':
      // Image already present or pulled elsewhere, so close out the pull line.
      return finalizeBotCreateTerminalLines(lines)
    case 'creating':
      return pushBotCreateTerminalLine(lines, { kind: 'creating', status: 'running' })
    case 'restoring':
      return pushBotCreateTerminalLine(lines, { kind: 'restoring', status: 'running' })
    case 'complete':
      return finalizeBotCreateTerminalLines(lines)
    case 'ready':
      return pushBotCreateTerminalLine(lines, { kind: 'ready', status: 'done' })
    case 'error': {
      const errored = finalizeBotCreateTerminalLines(lines, 'error')
      return [...errored, withId({ kind: 'error', status: 'error', message: event.message }, errored.length)]
    }
  }
}
