import {
  packageHasUpdates,
  packageInProgress,
  type PackageItem,
} from '@/composables/api/usePackages'

// What a Package row or page may ask the panel to do. The panel owns
// confirmation and streaming; these are only the choices.
export type PackageRowAction = 'open' | 'openSupermarket' | 'resume' | 'retry' | 'update' | 'viewProgress' | 'remove'

export interface PackagePrimaryAction {
  action: PackageRowAction
  labelKey: string
  variant: 'default' | 'outline'
  disabled: boolean
}

/** The one button a Package's state calls for, shared by the row and its page. */
export function packagePrimaryAction(
  item: PackageItem,
  options: { busy: boolean; ownsStream: boolean; readonly: boolean },
): PackagePrimaryAction | null {
  if (packageInProgress(item)) {
    if (!options.ownsStream) return null
    return { action: 'viewProgress', labelKey: 'packages.action.viewProgress', variant: 'outline', disabled: false }
  }
  if (item.status === 'failed') return { action: 'retry', labelKey: 'common.retry', variant: 'default', disabled: options.busy || options.readonly }
  if (item.status === 'partial') return { action: 'resume', labelKey: 'packages.action.resume', variant: 'default', disabled: options.busy || options.readonly }
  if (packageHasUpdates(item)) return { action: 'update', labelKey: 'packages.action.update', variant: 'default', disabled: options.busy || options.readonly }
  return null
}
