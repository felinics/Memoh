import {
  appKeyboardCommands,
  isAppKeyboardCommand,
  isKeyboardCommandInput,
  type AppKeyboardCommand,
  type KeyboardCommandInput,
} from '../../../web/src/lib/keyboard-commands'
import { acceleratorForCommand } from '../../../web/src/lib/keyboard-bindings'

export const DESKTOP_KEYBOARD_COMMAND_CHANNEL = 'desktop:keyboard-command'

export { appKeyboardCommands, isAppKeyboardCommand, isKeyboardCommandInput, acceleratorForCommand }
export type { AppKeyboardCommand, KeyboardCommandInput }
