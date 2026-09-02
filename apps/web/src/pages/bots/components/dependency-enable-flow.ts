import type { BotagentsBotAgent } from '@memohai/sdk'
import type {
  DependencyItem,
  PreflightItem,
  PreflightResponse,
} from '@/composables/api/useWorkspaceDependencies'
import { acpAgentDisplayName } from '@/utils/acp'

// Pure decision table of the "enable an agent" preflight (design §9.3). The
// flow component owns the dialogs; this module only maps the agent's declared
// dependency plus the Server's preflight answer onto the next step, so the
// branch list can be unit-tested without mounting anything. Agent CLIs are not
// pinned to a version: an installed copy passes whatever its version, so the
// only blocking states are "not installed" and "not available on this platform".

export interface EnableFlowRequirement {
  dependencyId: string
  /** Version hint the Server may still report; empty means "latest". */
  requiredVersion: string
}

export type EnableFlowStep =
  | { kind: 'satisfied' }
  /** WD-EXT-004: the UI guides the user to the workspace; it never starts it silently. */
  | { kind: 'workspace'; state: 'not_running' | 'missing' }
  | { kind: 'remote_offline' }
  | { kind: 'install'; item: DependencyItem }
  | { kind: 'platform_unsupported'; item: DependencyItem }
  /** The Server did not recognise the dependency or answered without a state. */
  | { kind: 'unknown' }

/** The dependency the runtime declares, or null for runtimes without one (ACP). */
export function agentDependencyRequirement(
  agent: Pick<BotagentsBotAgent, 'dependency'> | null | undefined,
): EnableFlowRequirement | null {
  const dependencyId = agent?.dependency?.dependency_id?.trim() ?? ''
  if (!dependencyId) return null
  return {
    dependencyId,
    requiredVersion: agent?.dependency?.required_version?.trim() ?? '',
  }
}

/**
 * Shapes the preflight answer like a list item so the shared confirm dialog
 * can render it: agents are always managed dependencies.
 */
export function dependencyItemFromPreflight(
  requirement: EnableFlowRequirement,
  preflight?: PreflightItem,
): DependencyItem {
  const state = preflight?.state
  return {
    id: requirement.dependencyId,
    name: preflight?.name?.trim() || acpAgentDisplayName(requirement.dependencyId, requirement.dependencyId),
    category: 'agent',
    source: 'managed',
    required_version: preflight?.required_version?.trim() || requirement.requiredVersion || undefined,
    installed_version: preflight?.installed_version?.trim() || undefined,
    status: state === 'satisfied' || state === 'version_mismatch' ? 'installed' : undefined,
    platform_supported: state !== 'platform_unsupported',
  }
}

export function resolveEnableFlowStep(
  requirement: EnableFlowRequirement,
  response: PreflightResponse | null | undefined,
): EnableFlowStep {
  switch (response?.workspace_state) {
    case 'not_running':
    case 'missing':
      return { kind: 'workspace', state: response.workspace_state }
    case 'remote_offline':
      return { kind: 'remote_offline' }
  }
  const preflight = (response?.items ?? []).find(item => item.dependency_id === requirement.dependencyId)
  if (!preflight) return { kind: 'unknown' }
  const item = dependencyItemFromPreflight(requirement, preflight)
  switch (preflight.state) {
    case 'satisfied':
    // No pin: any installed version is good enough to enable (WD-EXT-001).
    case 'version_mismatch':
      return { kind: 'satisfied' }
    case 'missing':
      return { kind: 'install', item }
    case 'platform_unsupported':
      return { kind: 'platform_unsupported', item }
    default:
      return { kind: 'unknown' }
  }
}
