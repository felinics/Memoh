import {
  appHasUpdates,
  appInProgress,
  type AppItem,
} from '@/composables/api/useApps'

// What an App row or page may ask the panel to do. The panel owns
// confirmation and streaming; these are only the choices.
export type AppRowAction = 'open' | 'openSupermarket' | 'resume' | 'retry' | 'update' | 'viewProgress' | 'remove' | 'tryIt'

export interface AppPrimaryAction {
  action: AppRowAction
  labelKey: string
  variant: 'default' | 'outline'
  disabled: boolean
}

/** The one button an App's state calls for, shared by the row and its page. */
export function appPrimaryAction(
  item: AppItem,
  options: { busy: boolean; ownsStream: boolean; readonly: boolean },
): AppPrimaryAction | null {
  if (appInProgress(item)) {
    if (!options.ownsStream) return null
    return { action: 'viewProgress', labelKey: 'apps.action.viewProgress', variant: 'outline', disabled: false }
  }
  // The persisted failed status does not identify the operation. Never
  // label materialization as a generic retry: it may undo a failed removal.
  if (item.status === 'failed') return { action: 'resume', labelKey: 'apps.action.restore', variant: 'default', disabled: options.busy || options.readonly }
  if (item.status === 'partial') return { action: 'resume', labelKey: 'apps.action.resume', variant: 'default', disabled: options.busy || options.readonly }
  if (appHasUpdates(item)) return { action: 'update', labelKey: 'apps.action.update', variant: 'default', disabled: options.busy || options.readonly }
  return null
}

/**
 * Whether to offer "Try it", which prefills a chat prompt asking the bot to
 * demo the App. Any App the workspace can run qualifies: one installed
 * through Memoh, or one discovered already present in the workspace (an
 * available update does not change that). Apps mid-operation, failed or
 * partially installed are left out until they are whole again.
 */
export function appCanTry(item: Pick<AppItem, 'status'>, options: { readonly: boolean }): boolean {
  return (item.status === 'installed' || item.status === 'discovered') && !options.readonly
}
