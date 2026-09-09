import type { SegmentedItem } from '@felinic/ui'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import { acpAgentDisplayName, normalizeACPAgentID } from '@/utils/acp'
import {
  BOT_AGENT_RUNTIME_CLAUDE_CODE,
  BOT_AGENT_RUNTIME_CODEX,
} from '@/utils/bot-agent'

// MEMOH_AGENT_VALUE is the built-in-agent segment's value in agent-type-pill.
// It is NOT an agent id — a reserved marker so the chooser can never collide
// with a normalized ACP profile id.
export const MEMOH_AGENT_VALUE = 'memoh'

// agentTypeItems builds the pill's segments: the built-in Memoh agent always
// first, then the direct external agent runtimes (codex, claude-code — they
// have no ACP profile), then one segment per ACP profile the server
// publishes.
export function agentTypeItems(profiles: AcpprofilePublicProfile[]): SegmentedItem<string>[] {
  const direct = [
    { value: BOT_AGENT_RUNTIME_CODEX, label: acpAgentDisplayName(BOT_AGENT_RUNTIME_CODEX, 'Codex') },
    { value: BOT_AGENT_RUNTIME_CLAUDE_CODE, label: acpAgentDisplayName(BOT_AGENT_RUNTIME_CLAUDE_CODE, 'Claude Code') },
  ]
  const agents = profiles.flatMap((profile) => {
    const agentId = normalizeACPAgentID(profile.id)
    // Skip entries that fail normalization or would collide with the reserved
    // built-in segment value or a direct runtime name.
    if (!agentId || agentId === MEMOH_AGENT_VALUE) return []
    if (agentId === BOT_AGENT_RUNTIME_CODEX || agentId === BOT_AGENT_RUNTIME_CLAUDE_CODE) return []
    return [{ value: agentId, label: profile.display_name || acpAgentDisplayName(agentId, agentId) }]
  })
  return [{ value: MEMOH_AGENT_VALUE, label: 'Memoh' }, ...direct, ...agents]
}
