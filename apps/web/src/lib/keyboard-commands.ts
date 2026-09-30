export const appKeyboardCommands = {
  closeCurrentWorkspaceTab: 'close-current-workspace-tab',
  saveActiveFile: 'save-active-file',
  toggleSidebar: 'toggle-sidebar',
  openSettings: 'open-settings',
  closeMediaLightbox: 'close-media-lightbox',
  mediaLightboxPrev: 'media-lightbox-prev',
  mediaLightboxNext: 'media-lightbox-next',
} as const

export type AppKeyboardCommand =
  typeof appKeyboardCommands[keyof typeof appKeyboardCommands]

const appKeyboardCommandValues = new Set<string>(Object.values(appKeyboardCommands))

export function isAppKeyboardCommand(value: unknown): value is AppKeyboardCommand {
  return typeof value === 'string' && appKeyboardCommandValues.has(value)
}

export type KeyboardCommandHandler = () => boolean | void

export interface KeyboardCommandApi {
  onKeyboardCommand(cb: (command: AppKeyboardCommand) => void): (() => void) | void
}

export type UnhandledKeyboardCommandCallback = (command: AppKeyboardCommand) => void

export interface KeyboardCommandRegistry {
  register(command: AppKeyboardCommand, handler: KeyboardCommandHandler): () => void
  dispatch(command: AppKeyboardCommand): boolean
  /**
   * Bridge an external command source (e.g. Electron IPC) into the registry.
   * `onUnhandled` fires for commands no registered handler claimed. Desktop uses
   * it to fall back to closing the window when there is no workspace tab to close.
   */
  connect(api: KeyboardCommandApi, onUnhandled?: UnhandledKeyboardCommandCallback): () => void
}

export interface ScopedKeyboardBinding {
  bind(): void
  unbind(): void
}

/**
 * An idempotent register/unregister pair for one command handler. Lets a caller
 * attach and detach the same handler repeatedly. Used by useKeyboardCommand to
 * keep a component's shortcut live only while it is mounted AND active, so a
 * KeepAlive-cached-but-deactivated component does not keep claiming the command.
 */
export function createScopedKeyboardBinding(
  registry: Pick<KeyboardCommandRegistry, 'register'>,
  command: AppKeyboardCommand,
  handler: KeyboardCommandHandler,
): ScopedKeyboardBinding {
  let unregister: (() => void) | null = null
  return {
    bind() {
      if (unregister) return
      unregister = registry.register(command, handler)
    },
    unbind() {
      if (!unregister) return
      unregister()
      unregister = null
    },
  }
}

export function createKeyboardCommandRegistry(): KeyboardCommandRegistry {
  const handlers = new Map<AppKeyboardCommand, Set<KeyboardCommandHandler>>()

  return {
    register(command, handler) {
      const commandHandlers = handlers.get(command) ?? new Set<KeyboardCommandHandler>()
      commandHandlers.add(handler)
      handlers.set(command, commandHandlers)
      return () => {
        commandHandlers.delete(handler)
        if (commandHandlers.size === 0) handlers.delete(command)
      }
    },

    dispatch(command) {
      const commandHandlers = handlers.get(command)
      if (!commandHandlers) return false
      for (const handler of commandHandlers) {
        if (handler() === true) return true
      }
      return false
    },

    connect(api, onUnhandled) {
      const unsubscribe = api.onKeyboardCommand((command) => {
        const handled = this.dispatch(command)
        if (!handled) onUnhandled?.(command)
      })
      return typeof unsubscribe === 'function' ? unsubscribe : () => {}
    },
  }
}
