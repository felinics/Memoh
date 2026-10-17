import { describe, expect, it } from 'vitest'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import { MEMOH_AGENT_VALUE, agentTypeItems } from './agent-type'

function profile(id: string, displayName = ''): AcpprofilePublicProfile {
  return { id, display_name: displayName } as AcpprofilePublicProfile
}

describe('agentTypeItems', () => {
  // A profile that normalizes to the reserved built-in value would replace the
  // Memoh segment's identity, and one that normalizes to a direct runtime name
  // would duplicate that segment — both are skipped, never rendered.
  it('skips profiles that collide with the reserved Memoh segment or a direct runtime', () => {
    const items = agentTypeItems([profile('Memoh'), profile('Claude-Code'), profile('gemini')])
    expect(items.map(item => item.value)).toEqual([MEMOH_AGENT_VALUE, 'codex', 'claude-code', 'gemini'])
  })
})
