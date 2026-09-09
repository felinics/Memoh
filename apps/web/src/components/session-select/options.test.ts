import { describe, expect, it } from 'vitest'
import type { SessionSession } from '@memohai/sdk'
import type { BotWorkdir } from '@/composables/api/useWorkdirs'
import { buildSessionOptions, sessionMark, type SessionSelectLabels } from './options'

const labels: SessionSelectLabels = {
  untitled: 'Untitled Session',
  recents: 'Recents',
  unavailableFolder: 'Unavailable folder',
  schedule: 'Scheduled Task',
  agent: 'Agent',
}

function session(id: string, overrides: Partial<SessionSession> = {}): SessionSession {
  return { id, bot_id: 'bot-1', title: `Session ${id}`, type: 'chat', session_mode: 'chat', ...overrides }
}

function workdir(id: string, name: string, overrides: Partial<BotWorkdir> = {}): BotWorkdir {
  return { id, name, path: `/data/${name}`, ...overrides }
}

describe('buildSessionOptions', () => {
  it('lists unfiled sessions without a header when there are no folders', () => {
    const options = buildSessionOptions([session('a'), session('b')], [], labels)

    expect(options.map(o => o.value)).toEqual(['a', 'b'])
    expect(options.every(o => o.group === '' && o.groupLabel === '')).toBe(true)
  })

  it('puts unfiled sessions first under Recents once a folder has sessions', () => {
    const options = buildSessionOptions(
      [session('filed', { workdir_id: 'wd-1' }), session('loose')],
      [workdir('wd-1', 'Docs')],
      labels,
    )

    expect(options.map(o => [o.value, o.groupLabel])).toEqual([
      ['loose', 'Recents'],
      ['filed', 'Docs'],
    ])
  })

  it('keeps folder order and drops folders with no sessions', () => {
    const options = buildSessionOptions(
      [session('b', { workdir_id: 'wd-2' }), session('a', { workdir_id: 'wd-1' })],
      [workdir('wd-1', 'First'), workdir('wd-empty', 'Empty'), workdir('wd-2', 'Second')],
      labels,
    )

    expect(options.map(o => o.groupLabel)).toEqual(['First', 'Second'])
  })

  it('keeps a session bound to a vanished folder in its own group', () => {
    const options = buildSessionOptions([session('a', { workdir_id: 'gone' })], [], labels)

    expect(options[0]?.group).toBe('gone')
    expect(options[0]?.groupLabel).toBe('Unavailable folder')
  })

  it('skips archived folders but still names the sessions still bound to them', () => {
    const options = buildSessionOptions(
      [session('a', { workdir_id: 'wd-1' })],
      [workdir('wd-1', 'Archived', { archived: true })],
      labels,
    )

    expect(options.map(o => o.groupLabel)).toEqual(['Archived'])
  })

  it('falls back to the untitled label and searches on title, folder and id', () => {
    const options = buildSessionOptions(
      [session('a', { title: '  ', workdir_id: 'wd-1' })],
      [workdir('wd-1', 'Docs')],
      labels,
    )

    expect(options[0]?.label).toBe('Untitled Session')
    expect(options[0]?.keywords).toEqual(['Untitled Session', 'Docs', 'a'])
  })
})

describe('sessionMark', () => {
  it('marks schedule sessions with the clock, even when they run an agent', () => {
    const mark = sessionMark(
      session('a', { session_mode: 'schedule', runtime_type: 'acp_agent', metadata: { acp_agent_id: 'codex' } }),
      labels,
    )

    expect(mark).toEqual({ kind: 'schedule', agentId: 'codex', label: 'Scheduled Task' })
  })

  it('marks ACP chats with the agent that runs them', () => {
    const mark = sessionMark(
      session('a', { runtime_type: 'acp_agent', runtime_metadata: { acp_agent_id: 'claude-code' } }),
      labels,
    )

    expect(mark.kind).toBe('acp')
    expect(mark.agentId).toBe('claude-code')
  })

  it('resolves the legacy acp_agent type with no runtime_type', () => {
    expect(sessionMark(session('a', { type: 'acp_agent', session_mode: '' }), labels).kind).toBe('acp')
  })

  it('leaves plain model chats unmarked', () => {
    expect(sessionMark(session('a'), labels).kind).toBe('chat')
  })
})
