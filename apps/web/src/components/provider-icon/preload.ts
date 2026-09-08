import { shallowRef, type ShallowRef } from 'vue'

// Share the fetched bytes, not just a detached Image that merely warms the
// browser's HTTP cache. A remounted icon can reuse this source without another
// remote resource request. Data URLs need no revocation while a consumer uses
// them; the bounded map limits how many unused sources we retain.
const sources = new Map<string, ShallowRef<string>>()
const maxEntries = 128
const maxBytes = 512 * 1024

export function providerIconSource(url: string): ShallowRef<string> {
  const cached = sources.get(url)
  if (cached) {
    sources.delete(url)
    sources.set(url, cached)
    return cached
  }
  const source = shallowRef('')
  sources.set(url, source)
  if (sources.size > maxEntries) sources.delete(sources.keys().next().value!)
  void load(url, source)
  return source
}

async function load(url: string, source: ShallowRef<string>): Promise<void> {
  try {
    const response = await fetch(url)
    if (!response.ok) throw new Error('Icon request failed')
    const blob = await response.blob()
    if (!blob.type.startsWith('image/') || blob.size > maxBytes) throw new Error('Icon cannot be cached')
    const bytes = new Uint8Array(await blob.arrayBuffer())
    const encoded = btoa(Array.from(bytes, byte => String.fromCharCode(byte)).join(''))
    const dataUrl = `data:${blob.type};base64,${encoded}`
    const image = new Image()
    image.src = dataUrl
    await image.decode()
    source.value = dataUrl
  } catch {
    // Some custom hosts permit img embedding but not CORS fetches. Keep those
    // working through the original URL and let a later mount retry the cache.
    source.value = url
    if (sources.get(url) === source) sources.delete(url)
  }
}

export function preloadProviderIcons(icons: Iterable<string | undefined>): void {
  if (typeof Image === 'undefined') return
  for (const icon of icons) {
    if (icon && /^https?:\/\//.test(icon)) providerIconSource(icon)
  }
}
