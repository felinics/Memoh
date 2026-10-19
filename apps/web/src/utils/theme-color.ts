// Keeps <meta name="theme-color"> in step with the page background the user
// actually sees. index.html ships a light/dark pair keyed on the OS preference;
// that is wrong once the user picks a theme or color scheme in settings, so the
// first sync replaces the pair with a single tag.
//
// The background token is authored in oklch. Browser support for oklch inside
// theme-color is uneven, so the computed color is rasterised to sRGB hex.

function toHex(color: string): string | null {
  const ctx = document.createElement('canvas').getContext('2d', { willReadFrequently: true })
  if (!ctx) return null
  ctx.fillStyle = color
  ctx.fillRect(0, 0, 1, 1)
  const [r, g, b] = ctx.getImageData(0, 0, 1, 1).data
  return `#${[r, g, b].map((v) => (v ?? 0).toString(16).padStart(2, '0')).join('')}`
}

export function syncThemeColorWithBackground(): void {
  if (typeof document === 'undefined' || !document.body) return
  const hex = toHex(getComputedStyle(document.body).backgroundColor)
  if (!hex) return
  const head = document.head
  const metas = head.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]')
  let meta = metas[0]
  metas.forEach((m, i) => { if (i > 0) m.remove() })
  if (!meta) {
    meta = document.createElement('meta')
    meta.name = 'theme-color'
    head.appendChild(meta)
  }
  meta.removeAttribute('media')
  meta.content = hex
}
