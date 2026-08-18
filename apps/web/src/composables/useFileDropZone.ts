import { onBeforeUnmount, onMounted, readonly, ref } from 'vue'

// OS-file drag state for one drop region (the sidebar Files panel, a chat pane).
// Presentation lives in file-drop-overlay.vue; this owns only the "is a file
// being dragged over me" question, where that region sits on screen, and the
// moment files land.
//
// WHY a composable and not a component: the two regions upload to completely
// different backends (container FS vs. composer attachments) but share the same
// fiddly event choreography — see the traps below. Only the choreography is
// shared.

export interface DropZoneBounds {
  left: number
  top: number
  width: number
  height: number
}

export interface FileDropZoneOptions {
  // Called with the raw DataTransfer on drop. Reading it is synchronous-only in
  // Chromium: `dataTransfer.items` is emptied the moment the drop handler
  // returns, so an async consumer MUST collect entries/files before its first
  // await (see utils/dropped-files.ts).
  onDrop: (transfer: DataTransfer) => void
  // Reactive gate — permission missing, chat read-only, an upload already
  // running. A disabled zone shows no overlay and refuses the drop, so the OS
  // paints its no-drop cursor instead of promising an upload that won't happen.
  disabled?: () => boolean
}

// A drag carries OS files iff its type list includes 'Files'. This is what keeps
// dockview's own panel drags (custom mime, no 'Files') from lighting up the
// upload overlay, and vice versa.
function carriesFiles(transfer: DataTransfer | null): boolean {
  return !!transfer && Array.from(transfer.types).includes('Files')
}

export function useFileDropZone(options: FileDropZoneOptions) {
  const active = ref(false)
  // Viewport box of this region, published for the overlay. The overlay is
  // full-bleed (it has to escape the region to cover the region's own chrome),
  // so the ONLY thing left saying "this half, not the other half" is where the
  // icon+label block sits — and that needs the region's geometry.
  const bounds = ref<DropZoneBounds | null>(null)

  function isDisabled(): boolean {
    return options.disabled?.() ?? false
  }

  function deactivate() {
    active.value = false
  }

  // Re-measured on every dragover rather than once on enter: a drag can outlive
  // a sidebar resize or a split being dragged. Writes only on a real change, or
  // the per-frame dragover would push a fresh object 60×/s and re-style the
  // anchor for nothing.
  function measure(host: EventTarget | null) {
    if (!(host instanceof HTMLElement)) return
    const rect = host.getBoundingClientRect()
    const prev = bounds.value
    if (prev
      && prev.left === rect.left && prev.top === rect.top
      && prev.width === rect.width && prev.height === rect.height) return
    bounds.value = { left: rect.left, top: rect.top, width: rect.width, height: rect.height }
  }

  // dragenter and dragover both claim the drag. dragover repeating every frame
  // is the self-healing part: as long as the pointer is inside, the flag is
  // continuously re-asserted, so a single missed dragenter can never leave the
  // overlay dark.
  function onDragEnter(event: DragEvent) {
    if (isDisabled() || !carriesFiles(event.dataTransfer)) return
    event.preventDefault()
    measure(event.currentTarget)
    active.value = true
  }

  function onDragOver(event: DragEvent) {
    if (isDisabled() || !carriesFiles(event.dataTransfer)) return
    // preventDefault is what makes this element a valid drop target at all —
    // without it the browser refuses the drop and navigates to the file
    // instead. It also marks the event as claimed, which is how the global
    // guard (lib/file-drop-guard.ts) knows to keep its hands off.
    event.preventDefault()
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy'
    measure(event.currentTarget)
    active.value = true
  }

  // Dragging across child elements fires a dragleave for every boundary
  // crossed, so a naive handler would strobe the overlay. Only a relatedTarget
  // outside this subtree is a real exit (it is null when the pointer leaves the
  // window entirely, which also reads as outside).
  function onDragLeave(event: DragEvent) {
    const host = event.currentTarget
    const next = event.relatedTarget
    if (host instanceof Node && next instanceof Node && host.contains(next)) return
    deactivate()
  }

  function onDrop(event: DragEvent) {
    if (!carriesFiles(event.dataTransfer)) return
    event.preventDefault()
    deactivate()
    if (isDisabled() || !event.dataTransfer) return
    options.onDrop(event.dataTransfer)
  }

  // Backstop. A drag that starts outside the page has no dragend here, so if the
  // pointer leaves through a gap the element's own dragleave never fires and the
  // overlay would stay up forever. A drop or a window-exit anywhere clears it.
  function onDocumentDragLeave(event: DragEvent) {
    if (event.relatedTarget === null) deactivate()
  }

  onMounted(() => {
    document.addEventListener('drop', deactivate)
    document.addEventListener('dragleave', onDocumentDragLeave)
  })

  onBeforeUnmount(() => {
    document.removeEventListener('drop', deactivate)
    document.removeEventListener('dragleave', onDocumentDragLeave)
  })

  return {
    active: readonly(active),
    bounds: readonly(bounds),
    // Spread onto the region's root element: v-on="dropZoneHandlers".
    handlers: {
      dragenter: onDragEnter,
      dragover: onDragOver,
      dragleave: onDragLeave,
      drop: onDrop,
    },
  }
}
