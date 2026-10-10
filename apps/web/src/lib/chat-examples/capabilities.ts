import type { BotCapability, BotCapabilitySnapshot } from './types'

/**
 * Raw results of the per-bot probes. A probe that failed or was not permitted
 * is left undefined; `container: null` means the container probe succeeded
 * and found nothing.
 */
export interface CapabilityProbes {
  settings?: {
    search_provider_id?: string
    memory_enabled?: boolean
    display_enabled?: boolean
  } | null
  container?: { status?: string } | null
}

/**
 * Turn probe results into a capability snapshot. Only facts the probes prove
 * are recorded: a failed probe stays unknown, which the UI treats as
 * available, so an example is never greyed out because of a 403 or a timeout.
 *
 * - `workspace` is only ever confirmed: remote and local runtimes have no
 *   container, so a missing container does not prove a missing workspace.
 * - `email` and `channel` have no per-bot probe and are never recorded.
 */
export function capabilitySnapshotFrom(probes: CapabilityProbes): BotCapabilitySnapshot {
  const snapshot: BotCapabilitySnapshot = { schedule: true }
  const settings = probes.settings
  if (settings) {
    snapshot.web_search = !!settings.search_provider_id
    if (typeof settings.memory_enabled === 'boolean') snapshot.memory = settings.memory_enabled
    if (typeof settings.display_enabled === 'boolean') snapshot.browser = settings.display_enabled
  }
  if (probes.container) snapshot.workspace = true
  return snapshot
}

/** Bot settings tab where each capability is turned on; null when there is nothing to configure. */
export const CAPABILITY_SETTINGS_TAB: Record<BotCapability, string | null> = {
  web_search: 'general',
  memory: 'memory',
  browser: 'advanced',
  workspace: 'container',
  email: 'apps',
  channel: 'channels',
  schedule: null,
}
