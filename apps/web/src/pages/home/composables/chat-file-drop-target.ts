import { shallowRef } from 'vue'

// The composer that receives files dropped ANYWHERE on the page (the
// page-level base drop zone in main-section forwards here).
//
// The composer owns its pending-attachment tray per chat pane instance
// (useComposerAttachments is per-pane), so "the composer" only means something
// once a pane claims it: a chat pane registers while it is the focused dock
// panel, and unregisters on blur/unmount. With split panes this makes the
// FOCUSED split the drop target; on pages with no chat pane (settings,
// onboarding) the slot is empty and the base zone stays inert — the global
// file-drop guard swallows those drops instead of navigating away.
//
// Registration is identity-guarded both ways because blur/focus ordering across
// two panes is not guaranteed: unregister only clears ITS OWN registration, so
// a late blur from the old pane can never evict the new pane's claim.

export interface ChatFileDropTarget {
  onDrop: (transfer: DataTransfer) => void
  // Mirrors the pane's own drop-zone gate (no bot, read-only, streaming, …) so
  // the base zone stays dark under exactly the same conditions.
  disabled: () => boolean
  // The pane's root element. The base zone measures it so its overlay anchors
  // over the pane the files will land in — never a detached window-centred
  // anchor (a third "center" the user rejected: at any moment the anchor is
  // either a region zone's rect or this pane's rect, and with split panes it
  // follows the focused split instead of floating between them).
  hostEl: () => HTMLElement | null
}

const target = shallowRef<ChatFileDropTarget | null>(null)

export function registerChatFileDropTarget(next: ChatFileDropTarget): () => void {
  target.value = next
  return () => {
    if (target.value === next) target.value = null
  }
}

export function getChatFileDropTarget(): ChatFileDropTarget | null {
  return target.value
}
