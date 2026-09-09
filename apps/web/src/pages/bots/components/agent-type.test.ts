import { describe, expect, it } from 'vitest'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import { MEMOH_AGENT_VALUE, agentTypeItems } from './agent-type'

function profile(id: string, displayName = ''): AcpprofilePublicProfile {
  return { id, display_name: displayName } as AcpprofilePublicProfile
}

describe('agentTypeItems', () => {
  // codex / claude-code are direct runtimes with no ACP profile; they carry
  // their brand names and never show a raw slug.
  // A leftover profile whose id normalizes to a direct runtime name would
  // duplicate that segment — it must be skipped.
  // A profile that normalizes to the reserved built-in value would replace the
  // Memoh segment's identity — it must be skipped, never rendered.
  it('skips profiles that collide with the reserved Memoh segment', () => {
    const items = agentTypeItems([profile('Memoh')])
    expect(items.map(item => item.value)).toEqual([MEMOH_AGENT_VALUE, 'codex', 'claude-code'])
  })
})
