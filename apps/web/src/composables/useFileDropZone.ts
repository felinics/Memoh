import { onBeforeUnmount, onMounted, readonly, ref } from 'vue'

// OS-file drag state for one drop region (the sidebar Files panel, a chat pane,
// the page-level base zone in main-section).
// Presentation lives in file-drop-overlay.vue; this owns only the "is a file
// being dragged over me" question, where that region sits on screen, and the
// moment files land.
//
// WHY a composable and not a component: the regions upload to completely
// different backends (container FS vs. composer attachments) but share the same
// fiddly event choreography — see the traps below. Only the choreography is
// shared.
//
// ZONES NEST. The page-level base zone ("files dropped anywhere go to the
// composer") sits UNDER every region zone (chat pane, Files view), and drag
// events bubble child → parent. Two rules keep exactly one overlay lit:
//  1. A CLAIMING zone calls stopPropagation, so an outer zone never sees events
//     over an inner region. A disabled zone does NOT stop — the event bubbles
//     out and the base zone can still answer for that space.
//  2. A claim also EVICTS the previous claimant (below). The pointer can cross
//     from an outer-owned region straight into an inner one, and the outer's
//     dragleave never fires in that case (relatedTarget is still inside the
//     outer host), so without eviction both overlays would stay lit.

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
  // Measure THIS element for the published bounds instead of the zone's own
  // host. Exists for the page-level base zone: its host is the whole window,
  // but its anchor must sit over the pane that will receive the files.
  measureTarget?: () => HTMLElement | null
}

// A drag carries OS files iff its type list includes 'Files'. This is what keeps
// dockview's own panel drags (custom mime, no 'Files') from lighting up the
// upload overlay, and vice versa.
function carriesFiles(transfer: DataTransfer | null): boolean {
  return !!transfer && Array.from(transfer.types).includes('Files')
}

// The single currently-lit zone, evicted by the next claim (nesting rule 2 in
// the header). Module-level: claimants live in unrelated components.
let claimant: (() => void) | null = null

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
    if (claimant === deactivate) claimant = null
  }

  // Claim the drag for this zone: light the overlay, evict the previous
  // claimant, and stop the event so outer zones never see it.
  function claim(event: DragEvent) {
    cancelExit()
    if (claimant && claimant !== deactivate) claimant()
    claimant = deactivate
    event.preventDefault()
    event.stopPropagation()
    measure(event.currentTarget)
    active.value = true
  }

  // "Real exit" signals are debounced: Chrome fires dragleave with
  // relatedTarget null at random SUBTREE boundaries (it should mean
  // left-the-window only), and the next dragover re-claims a frame later —
  // without the debounce the overlay strobes off/on at region edges, which in
  // dark mode reads as a black flash (the cover there IS near-black). A
  // genuine window exit produces no further dragover, so 80ms of silence is
  // the confirmation; dragover fires within milliseconds while moving.
  let exitTimer: ReturnType<typeof setTimeout> | undefined

  function cancelExit() {
    if (exitTimer === undefined) return
    clearTimeout(exitTimer)
    exitTimer = undefined
  }

  function scheduleExit() {
    cancelExit()
    exitTimer = setTimeout(() => {
      exitTimer = undefined
      deactivate()
    }, 80)
  }

  // Re-measured on every dragover rather than once on enter: a drag can outlive
  // a sidebar resize or a split being dragged. Writes only on a real change, or
  // the per-frame dragover would push a fresh object 60×/s and re-style the
  // anchor for nothing.
  function measure(host: EventTarget | null) {
    const el = options.measureTarget?.() ?? (host instanceof HTMLElement ? host : null)
    if (!el) return
    const rect = el.getBoundingClientRect()
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
    claim(event)
  }

  function onDragOver(event: DragEvent) {
    if (isDisabled() || !carriesFiles(event.dataTransfer)) return
    // preventDefault (inside claim) is what makes this element a valid drop
    // target at all — without it the browser refuses the drop and navigates to
    // the file instead. It also marks the event as claimed, which is how the
    // global guard (lib/file-drop-guard.ts) knows to keep its hands off.
    claim(event)
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy'
  }

  // Dragging across child elements fires a dragleave for every boundary
  // crossed, so a naive handler would strobe the overlay. Only a relatedTarget
  // outside this subtree is a real exit, and even that goes through the
  // debounce above (null relatedTarget is not trustworthy mid-drag).
  function onDragLeave(event: DragEvent) {
    const host = event.currentTarget
    const next = event.relatedTarget
    if (host instanceof Node && next instanceof Node && host.contains(next)) return
    scheduleExit()
  }

  function onDrop(event: DragEvent) {
    if (!carriesFiles(event.dataTransfer)) return
    event.preventDefault()
    cancelExit()
    deactivate()
    // A disabled zone lets the drop bubble: an outer zone may still accept it
    // (its overlay is the one lit in that case — see the nesting rules above).
    if (isDisabled() || !event.dataTransfer) return
    event.stopPropagation()
    options.onDrop(event.dataTransfer)
  }

  // Backstop. A drag that starts outside the page has no dragend here, so if the
  // pointer leaves through a gap the element's own dragleave never fires and the
  // overlay would stay up forever. A drop or a window-exit anywhere clears it.
  function onDocumentDragLeave(event: DragEvent) {
    if (event.relatedTarget === null) scheduleExit()
  }

  onMounted(() => {
    document.addEventListener('drop', deactivate)
    document.addEventListener('dragleave', onDocumentDragLeave)
  })

  onBeforeUnmount(() => {
    document.removeEventListener('drop', deactivate)
    document.removeEventListener('dragleave', onDocumentDragLeave)
    cancelExit()
    // A zone that vanishes mid-drag (e.g. the sidebar leaves the Files view)
    // must release the claimant slot, or it blocks every other zone's claim.
    deactivate()
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
