import { reactive } from 'vue'

const CACHE_LIMIT = 256
const SAMPLE_SIZE = 16
const darkInvertState = reactive<Record<string, boolean>>({})
const cacheOrder: string[] = []
let sharedCtx: CanvasRenderingContext2D | null | undefined

function getSampleContext(): CanvasRenderingContext2D | null {
  if (sharedCtx !== undefined) return sharedCtx
  if (typeof document === 'undefined') {
    sharedCtx = null
    return null
  }
  const canvas = document.createElement('canvas')
  canvas.width = SAMPLE_SIZE
  canvas.height = SAMPLE_SIZE
  sharedCtx = canvas.getContext('2d', { willReadFrequently: true })
  return sharedCtx
}

function rememberDecision(key: string, value: boolean): void {
  if (!key) return
  if (!(key in darkInvertState)) {
    cacheOrder.push(key)
    if (cacheOrder.length > CACHE_LIMIT) {
      const evicted = cacheOrder.shift()
      if (evicted) delete darkInvertState[evicted]
    }
  }
  darkInvertState[key] = value
}

/** Detect unthemed dark monochrome icons so dark mode can invert them without disturbing brand-colored artwork. */
export function detectDarkMonochromeImage(img: HTMLImageElement, src?: string): boolean {
  const key = src || img.getAttribute('src') || img.currentSrc || ''
  if (key && key in darkInvertState) return Boolean(darkInvertState[key])

  if (!img.complete || img.naturalWidth === 0 || img.naturalHeight === 0) return false
  const ctx = getSampleContext()
  if (!ctx) return false

  try {
    ctx.clearRect(0, 0, SAMPLE_SIZE, SAMPLE_SIZE)
    ctx.drawImage(img, 0, 0, SAMPLE_SIZE, SAMPLE_SIZE)
    const { data } = ctx.getImageData(0, 0, SAMPLE_SIZE, SAMPLE_SIZE)
    let weightSum = 0
    let lumSum = 0
    let chromaSum = 0
    let chromaticWeight = 0

    for (let i = 0; i < data.length; i += 4) {
      const a = data[i + 3] ?? 0
      if (a < 32) continue
      const w = a / 255
      const r = (data[i] ?? 0) / 255
      const g = (data[i + 1] ?? 0) / 255
      const b = (data[i + 2] ?? 0) / 255
      const max = Math.max(r, g, b)
      const min = Math.min(r, g, b)
      const chroma = max - min
      const lum = 0.2126 * r + 0.7152 * g + 0.0722 * b
      weightSum += w
      lumSum += lum * w
      chromaSum += chroma * w
      if (chroma >= 0.2) chromaticWeight += w
    }

    if (weightSum === 0) {
      rememberDecision(key, false)
      return false
    }

    const avgLum = lumSum / weightSum
    const avgChroma = chromaSum / weightSum
    const chromaticRatio = chromaticWeight / weightSum
    const isDarkMonochrome = avgLum < 0.35 && avgChroma < 0.08 && chromaticRatio < 0.08
    rememberDecision(key, isDarkMonochrome)
    return isDarkMonochrome
  } catch {
    rememberDecision(key, false)
    return false
  }
}

export function shouldInvertDarkIcon(src?: string | null): boolean {
  return src ? Boolean(darkInvertState[src]) : false
}

export function handleDarkIconLoad(event: Event, enabled = true): void {
  if (!enabled) return
  const img = event.currentTarget
  if (!(img instanceof HTMLImageElement)) return
  detectDarkMonochromeImage(img)
}
