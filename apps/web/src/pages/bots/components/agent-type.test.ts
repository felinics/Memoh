import { describe, expect, it } from 'vitest'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import { MEMOH_AGENT_VALUE, agentTypeItems } from './agent-type'

function profile(id: string, displayName = ''): AcpprofilePublicProfile {
  return { id, display_name: displayName } as AcpprofilePublicProfile
}

describe('agentTypeItems', () => {
  it('always puts the built-in Memoh agent first', () => {
    const items = agentTypeItems([profile('codex'), profile('claude-code')])
    expect(items[0]).toEqual({ value: MEMOH_AGENT_VALUE, label: 'Memoh' })
    expect(items.map(item => item.value)).toEqual([MEMOH_AGENT_VALUE, 'codex', 'claude-code'])
  })

  // Known agents get their brand name even without a server display_name —
  // the picker never shows a raw slug like "claude-code".
  it('falls back to the brand display name for known agents', () => {
    const items = agentTypeItems([profile('claude-code')])
    expect(items[1]?.label).toBe('Claude Code')
  })

  it('prefers the server-provided display name', () => {
    const items = agentTypeItems([profile('codex', 'Codex (managed)')])
    expect(items[1]?.label).toBe('Codex (managed)')
  })

  it('normalizes profile ids the same way ACP metadata does', () => {
    const items = agentTypeItems([profile('  Claude-Code ')])
    expect(items.map(item => item.value)).toContain('claude-code')
  })

  // A profile that normalizes to the reserved built-in value would replace the
  // Memoh segment's identity — it must be skipped, never rendered.
  it('skips profiles that collide with the reserved Memoh segment', () => {
    const items = agentTypeItems([profile('Memoh')])
    expect(items).toHaveLength(1)
  })

  // Fail-closed: no published profiles → a single (hidden by the pill)
  // built-in segment, so surfaces behave as if the chooser didn't exist.
  it('returns only the built-in segment when no profiles are published', () => {
    expect(agentTypeItems([])).toEqual([{ value: MEMOH_AGENT_VALUE, label: 'Memoh' }])
  })
})
