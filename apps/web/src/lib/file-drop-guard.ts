// Global backstop for OS file drops that no drop zone claimed.
//
// A file dropped on a page that doesn't handle it is a NAVIGATION: the browser
// leaves the SPA and opens the file, taking unsent composer text and the whole
// dock layout with it. On desktop the same drop hits the Electron shell's
// will-navigate guard instead, which is equally not what the user meant.
//
// So: anything the app doesn't explicitly accept gets swallowed, with the OS
// no-drop cursor as the answer. "Claimed" is read off `defaultPrevented` — a
// real drop zone preventDefaults `dragover` to become a valid target, and this
// listener runs in the bubble phase, after the target's own handlers.
//
// Installed once per renderer (apps/web/src/main.ts and the desktop renderer
// bootstrap), not per component: the pages without a drop zone — login,
// onboarding, settings — are exactly the ones that need the guard.

// Native file inputs accept drops through the browser's own default action, with
// no script preventDefault to detect. Swallowing their dragover would break
// dropping onto a visible <input type="file">, so they're exempt.
function isNativeFileInput(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false
  const input = target.closest('input[type="file"]')
  return input !== null
}

function carriesFiles(transfer: DataTransfer | null): boolean {
  return !!transfer && Array.from(transfer.types).includes('Files')
}

function shouldSwallow(event: DragEvent): boolean {
  if (event.defaultPrevented) return false
  if (!carriesFiles(event.dataTransfer)) return false
  return !isNativeFileInput(event.target)
}

export function installFileDropGuard(): void {
  document.addEventListener('dragover', (event) => {
    if (!shouldSwallow(event)) return
    event.preventDefault()
    // Honest cursor: the drop will do nothing, so don't promise a copy.
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'none'
  })

  document.addEventListener('drop', (event) => {
    if (!shouldSwallow(event)) return
    event.preventDefault()
  })
}
