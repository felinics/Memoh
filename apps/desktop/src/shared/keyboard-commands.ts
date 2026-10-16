import {
  appKeyboardCommands,
  isAppKeyboardCommand,
  type AppKeyboardCommand,
} from '../../../web/src/lib/keyboard-commands'
import { acceleratorForCommand, toElectronAccelerator, type KeyboardPlatform } from '../../../web/src/lib/keyboard-bindings'
import { shortcutKeyFromEvent } from '../../../web/src/lib/keyboard-combo'

export const DESKTOP_KEYBOARD_COMMAND_CHANNEL = 'desktop:keyboard-command'

export { appKeyboardCommands, isAppKeyboardCommand, acceleratorForCommand }
export type { AppKeyboardCommand }

type KeyboardCommandInput = Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey'> & { code?: string }

export function matchesMenuAccelerator(input: KeyboardCommandInput, accelerator: string | undefined, platform: KeyboardPlatform): boolean {
  if (platform === 'mac' ? input.ctrlKey : input.metaKey) return false
  return [input.key, shortcutKeyFromEvent(input, platform === 'mac')].some(key => toElectronAccelerator({
    key, mod: platform === 'mac' ? input.metaKey : input.ctrlKey,
    alt: input.altKey, shift: input.shiftKey,
  }) === accelerator)
}
