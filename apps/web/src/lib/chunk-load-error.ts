// A dynamically imported chunk can fail to load when the network drops, the
// user aborts a reload, or a new deployment has replaced the hashed file the
// running page still points at. Browsers report it with these messages
// (Chromium, WebKit, Firefox); a page reload fetches the current index.html
// and its chunks.
export function isChunkLoadError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error)
  return message.includes('Failed to fetch dynamically imported module')
    || message.includes('Importing a module script failed')
    || message.includes('error loading dynamically imported module')
}
