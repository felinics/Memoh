export interface ComposerInputHandle {
  element: HTMLElement | null
  disabled: boolean
  focus: (options?: FocusOptions) => void
  insertText: (text: string) => void
  paste: (event: ClipboardEvent) => void
}

export type ComposerInputTarget = HTMLTextAreaElement | ComposerInputHandle
