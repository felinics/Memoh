// Keep decoded artwork ready across menu mounts. Failed requests are removable
// so a later catalog refresh can retry instead of caching a failed image forever.
const images = new Map<string, HTMLImageElement>()

export function preloadProviderIcons(icons: Iterable<string | undefined>): void {
  if (typeof Image === 'undefined') return
  for (const icon of icons) {
    if (!icon || !/^https?:\/\//.test(icon) || images.has(icon)) continue
    const image = new Image()
    images.set(icon, image)
    image.onerror = () => { images.delete(icon) }
    image.src = icon
    void image.decode().catch(() => { images.delete(icon) })
  }
}
