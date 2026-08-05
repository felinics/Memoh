import type { SegmentedItem } from '@felinic/ui'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import { acpAgentDisplayName, normalizeACPAgentID } from '@/utils/acp'

// MEMOH_AGENT_VALUE is the built-in-agent segment's value in agent-type-pill.
// It is NOT an agent id — a reserved marker so the chooser can never collide
// with a normalized ACP profile id.
export const MEMOH_AGENT_VALUE = 'memoh'

// agentTypeItems builds the pill's segments: the built-in Memoh agent always
// first, then one segment per ACP profile the server publishes. Callers hide
// the pill entirely when this returns a single segment (no hosted agents on
// this server), so the surface behaves exactly as if the chooser didn't exist.
export function agentTypeItems(profiles: AcpprofilePublicProfile[]): SegmentedItem<string>[] {
  const agents = profiles.flatMap((profile) => {
    const agentId = normalizeACPAgentID(profile.id)
    // Skip entries that fail normalization or would collide with the reserved
    // built-in segment value.
    if (!agentId || agentId === MEMOH_AGENT_VALUE) return []
    return [{ value: agentId, label: profile.display_name || acpAgentDisplayName(agentId, agentId) }]
  })
  return [{ value: MEMOH_AGENT_VALUE, label: 'Memoh' }, ...agents]
}
