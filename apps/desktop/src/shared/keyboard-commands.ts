import {
  appKeyboardCommands,
  isAppKeyboardCommand,
  isKeyboardCommandInput,
  type AppKeyboardCommand,
  type KeyboardCommandInput,
} from '../../../web/src/lib/keyboard-commands'
import { acceleratorForCommand, toElectronAccelerator, type KeyboardPlatform } from '../../../web/src/lib/keyboard-bindings'

export const DESKTOP_KEYBOARD_COMMAND_CHANNEL = 'desktop:keyboard-command'

export { appKeyboardCommands, isAppKeyboardCommand, isKeyboardCommandInput, acceleratorForCommand }
export type { AppKeyboardCommand, KeyboardCommandInput }

export function matchesMenuAccelerator(input: KeyboardCommandInput, accelerator: string | undefined, platform: KeyboardPlatform): boolean {
  if (platform === 'mac' ? input.ctrlKey : input.metaKey) return false
  return toElectronAccelerator({
    key: input.key, mod: platform === 'mac' ? input.metaKey : input.ctrlKey,
    alt: input.altKey, shift: input.shiftKey,
  }) === accelerator
}
