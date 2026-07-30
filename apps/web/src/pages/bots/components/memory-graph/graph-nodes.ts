import type { AccentPair, ChartTheme } from './types'

export function parseRgbChannels(color: string): [number, number, number] | null {
  const match = color.match(/rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)/i)
  if (!match) return null
  return [Number(match[1]), Number(match[2]), Number(match[3])]
}

export function subjectAccent(subject: string | undefined, theme: ChartTheme): AccentPair {
  if (!subject || theme.palette.length === 0) return theme.fallback
  let hash = 0
  for (let i = 0; i < subject.length; i++) {
    hash = ((hash << 5) - hash + subject.charCodeAt(i)) | 0
  }
  return theme.palette[Math.abs(hash) % theme.palette.length] ?? theme.fallback
}

export function graphNodeSize(count: number | undefined): number {
  const value = Number(count)
  if (!Number.isFinite(value) || value <= 1) return 34
  return Math.min(48, 34 + Math.log2(value) * 5)
}

export function particleItemStyle(accent: AccentPair) {
  const disk = parseRgbChannels(accent.disk) ?? parseRgbChannels(accent.core) ?? [182, 212, 243]
  const core = parseRgbChannels(accent.core) ?? disk
  const diskFill = `rgba(${disk[0]}, ${disk[1]}, ${disk[2]}, 0.30)`
  const coreFill = `rgb(${core[0]}, ${core[1]}, ${core[2]})`
  return {
    color: {
      type: 'radial' as const,
      x: 0.5,
      y: 0.5,
      r: 0.5,
      colorStops: [
        { offset: 0, color: coreFill },
        { offset: 0.13, color: coreFill },
        { offset: 0.14, color: diskFill },
        { offset: 1, color: diskFill },
      ],
    },
    borderWidth: 0,
  }
}

export function readResolvedColor(
  resolved: string,
  canvas: CanvasRenderingContext2D | null,
): string {
  if (!resolved) return resolved
  if (!canvas) return resolved
  try {
    canvas.clearRect(0, 0, 1, 1)
    canvas.fillStyle = '#000'
    canvas.fillStyle = resolved
    canvas.fillRect(0, 0, 1, 1)
    const [r = 0, g = 0, b = 0, a = 255] = canvas.getImageData(0, 0, 1, 1).data
    return a === 255 ? `rgb(${r}, ${g}, ${b})` : `rgba(${r}, ${g}, ${b}, ${(a / 255).toFixed(3)})`
  }
  catch {
    return resolved
  }
}
