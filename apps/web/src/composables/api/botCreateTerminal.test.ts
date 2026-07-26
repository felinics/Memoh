import { describe, expect, it } from 'vitest'
import {
  appendBotCreateTerminalLine,
  finalizeBotCreateTerminalLines,
  pushBotCreateTerminalLine,
  type BotCreateTerminalLine,
} from './botCreateTerminal'
import type { BotCreateStreamEvent } from './useBotCreateStream'

describe('botCreateTerminal', () => {
  it('pushes a running pulling line carrying the image', () => {
    const lines = appendBotCreateTerminalLine([], { type: 'pulling', image: 'ghcr.io/memoh:latest' })

    expect(lines).toHaveLength(1)
    expect(lines[0]).toMatchObject({ kind: 'pulling', status: 'running', image: 'ghcr.io/memoh:latest' })
  })

  it('updates the trailing pulling line percent in place on pull_progress', () => {
    let lines = appendBotCreateTerminalLine([], { type: 'pulling', image: 'img' })
    lines = appendBotCreateTerminalLine(lines, {
      type: 'pull_progress',
      layers: [{ ref: 'a', offset: 50, total: 100 }],
    })

    expect(lines).toHaveLength(1)
    expect(lines[0]).toMatchObject({ kind: 'pulling', status: 'running', percent: 50 })
  })

  it('finalizes the pulling line and pushes a running creating line', () => {
    let lines = appendBotCreateTerminalLine([], { type: 'pulling', image: 'img' })
    lines = appendBotCreateTerminalLine(lines, { type: 'creating' })

    expect(lines.map(l => ({ kind: l.kind, status: l.status }))).toEqual([
      { kind: 'pulling', status: 'done' },
      { kind: 'creating', status: 'running' },
    ])
  })

  it('marks the trailing running line as error and appends an error line', () => {
    let lines = appendBotCreateTerminalLine([], { type: 'creating' })
    lines = appendBotCreateTerminalLine(lines, { type: 'error', message: 'boom' })

    expect(lines.map(l => ({ kind: l.kind, status: l.status }))).toEqual([
      { kind: 'creating', status: 'error' },
      { kind: 'error', status: 'error' },
    ])
    expect(lines.at(1)?.message).toBe('boom')
  })

  it('derives the full happy-path log', () => {
    let lines: BotCreateTerminalLine[] = []
    const events: BotCreateStreamEvent[] = [
      { type: 'bot_created', bot: { id: 'b1' } },
      { type: 'pulling', image: 'img' },
      { type: 'pull_progress', layers: [{ ref: 'a', offset: 100, total: 100 }] },
      { type: 'creating' },
      { type: 'restoring' },
      {
        type: 'complete',
        container: {
          container_id: 'workspace-b1',
          workspace_backend: 'container',
          runtime_backend: 'io.containerd.runc.v2',
          started: true,
        },
      },
      { type: 'ready', bot: { id: 'b1' } },
    ]
    for (const event of events) {
      lines = appendBotCreateTerminalLine(lines, event)
    }

    expect(lines.map(l => ({ kind: l.kind, status: l.status }))).toEqual([
      { kind: 'bot-created', status: 'done' },
      { kind: 'pulling', status: 'done' },
      { kind: 'creating', status: 'done' },
      { kind: 'restoring', status: 'done' },
      { kind: 'ready', status: 'done' },
    ])
  })

  it('pushBotCreateTerminalLine finalizes the prior running line to done', () => {
    let lines = appendBotCreateTerminalLine([], { type: 'creating' })
    lines = pushBotCreateTerminalLine(lines, { kind: 'applying-settings', status: 'running' })

    expect(lines.map(l => ({ kind: l.kind, status: l.status }))).toEqual([
      { kind: 'creating', status: 'done' },
      { kind: 'applying-settings', status: 'running' },
    ])
  })

  it('finalizeBotCreateTerminalLines marks the trailing running line done', () => {
    let lines = pushBotCreateTerminalLine([], { kind: 'applying-settings', status: 'running' })
    lines = finalizeBotCreateTerminalLines(lines)

    expect(lines[0]).toMatchObject({ kind: 'applying-settings', status: 'done' })
  })
})
