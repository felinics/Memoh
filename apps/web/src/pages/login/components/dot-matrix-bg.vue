<template>
  <div
    class="size-full text-foreground"
    aria-hidden="true"
  >
    <canvas
      ref="ambientCanvasEl"
      class="absolute inset-0 size-full"
    />
    <canvas
      ref="canvasEl"
      class="absolute inset-0 size-full"
    />
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import {
  STAGE_DUR,
  STAGE_FR,
  STAGE_OP,
  THEMES,
  type KeyframeTrack,
  type ShapeDef,
} from './dot-matrix-keyframes'

/**
 * 点阵背景:归一化关键帧轨迹驱动的几何形状,在常亮点网格上轮流点亮。
 * 形状与键帧(dot-matrix-keyframes.ts)是初版原样;本文件的渲染管线
 * 重写过一次,解决初版两大问题——
 *   1. CPU 70-80%:初版每帧对全屏尺寸的离屏画布 getImageData(≈2M 像素)
 *      再逐点画 ~1.5 万个独立 arc/fill。现在离屏"场"画布直接用点阵分辨率
 *      (1 像素 = 1 个点,抗锯齿天然充当覆盖率采样),getImageData 缩到
 *      ~1 万像素;常亮层和完整点阵在 resize 时栅格化为位图;每帧只更新
 *      点阵分辨率的 alpha 遮罩并合成亮区,不再为数千个亮点重建 Path2D;
 *      再加 30fps 帧率上限。
 *   2. 节奏太慢:初版 MOTION_RATE=0.55 全局降速,现在按键帧原速(1.0)播放。
 * 颜色裁决:亮暗都不带彩——初版暗色用 accent 调色板逐点上色,已否决;
 * 两种主题统一用 text-foreground 单色,暗色仅调点亮增益。
 * 使用方:SaaS 登录页 + 实例创建/等待/无工作区页面。
 */

/** 点间距(CSS px) */
const GAP = 13.5
const DOT_RADIUS = 1
/** 点亮覆盖率量化档数(也是每帧点亮层的最大 fill 次数) */
const LEVELS = 4
/** 键帧播放速率;1 = 按 keyframes 原速 */
const MOTION_RATE = 1
/** 归一化坐标映射到视口时的内边距(含形状半径余量) */
const VIEW_INSET = 0.08
/** 帧率上限:背景动效 30fps 足够顺滑,渲染开销直接减半 */
const FRAME_MS = 1000 / 30

// 网格亮度(点色 × alpha),亮暗同值(设计裁决:暗色不单独加亮)
const AMBIENT = 0.105
// 点亮增益:暗色白点需要更高 alpha 才能与亮色的黑点等感知强度
const GAIN_LIGHT = 0.32
const GAIN_DARK = 0.44

const canvasEl = ref<HTMLCanvasElement | null>(null)
const ambientCanvasEl = ref<HTMLCanvasElement | null>(null)

let raf = 0
let ctx: CanvasRenderingContext2D | null = null
/** 点阵分辨率的离屏"场":alpha 通道即每个点的点亮覆盖率 */
let field: HTMLCanvasElement | null = null
let fieldCtx: CanvasRenderingContext2D | null = null
let cols = 0
let rows = 0
let viewW = 0
let viewH = 0
let dpr = 1
let dotColor = 'currentColor'
let isDark = false
/** 尺寸不变时可直接复用的常亮层和完整点阵层 */
let dotLayer: HTMLCanvasElement | null = null
/** 点阵分辨率的量化 alpha 遮罩,每帧只改它的像素 */
let mask: HTMLCanvasElement | null = null
let maskCtx: CanvasRenderingContext2D | null = null
let maskImage: ImageData | null = null
let lastNow = 0
let lastFrameAt = 0
let reduceMotion = false
let animTime = 0
let pageVisible = true

/** 归一化 [0,1] → 场像素,始终铺满当前窗口(含超宽/竖屏) */
function normToPx(nx: number, ny: number, w: number, h: number): [number, number] {
  const span = 1 - 2 * VIEW_INSET
  return [
    (VIEW_INSET + nx * span) * w,
    (VIEW_INSET + ny * span) * h,
  ]
}

function sampleTrack(track: KeyframeTrack, frame: number, comp = 0): number {
  if (track.static !== undefined) {
    const v = track.static
    return typeof v === 'number' ? v : (v[comp] ?? 0)
  }
  const frames = track.frames!
  if (!frames.length) return 0
  let i = 0
  while (i < frames.length - 1 && frames[i + 1]!.t <= frame) i++
  const a = frames[i]!
  const b = frames[Math.min(i + 1, frames.length - 1)]!
  if (a.t === b.t) {
    const v = a.v
    return typeof v === 'number' ? v : (v[comp] ?? 0)
  }
  const t = (frame - a.t) / (b.t - a.t)
  const va = a.v
  const vb = b.v
  const na = typeof va === 'number' ? va : (va[comp] ?? 0)
  const nb = typeof vb === 'number' ? vb : (vb[comp] ?? 0)
  return na + (nb - na) * t
}

function readColor() {
  if (!canvasEl.value) return
  const cs = getComputedStyle(canvasEl.value)
  dotColor = cs.color || 'currentColor'
  isDark = document.documentElement.classList.contains('dark')
}

function createLayer(width: number, height: number) {
  const layer = document.createElement('canvas')
  layer.width = width
  layer.height = height
  return layer
}

function rebuildDotLayers() {
  const width = Math.max(1, Math.round(viewW * dpr))
  const height = Math.max(1, Math.round(viewH * dpr))
  dotLayer = createLayer(width, height)
  const ambientLayer = ambientCanvasEl.value
  if (!ambientLayer) return
  ambientLayer.width = width
  ambientLayer.height = height

  const dotCtx = dotLayer.getContext('2d')
  const ambientCtx = ambientLayer.getContext('2d')
  if (!dotCtx || !ambientCtx) return

  const path = new Path2D()
  const twoPi = Math.PI * 2
  for (let y = 0; y < rows; y++) {
    const py = y * GAP
    for (let x = 0; x < cols; x++) {
      const px = x * GAP
      path.moveTo(px + DOT_RADIUS, py)
      path.arc(px, py, DOT_RADIUS, 0, twoPi)
    }
  }

  for (const layerCtx of [dotCtx, ambientCtx]) {
    layerCtx.setTransform(dpr, 0, 0, dpr, 0, 0)
    layerCtx.fillStyle = dotColor
  }
  dotCtx.fill(path)
  ambientCtx.globalAlpha = AMBIENT
  ambientCtx.fill(path)
  ambientCtx.globalAlpha = 1
}

function resize() {
  const el = canvasEl.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  viewW = Math.max(1, rect.width)
  viewH = Math.max(1, rect.height)
  dpr = Math.min(window.devicePixelRatio || 1, 2)
  el.width = Math.max(1, Math.round(viewW * dpr))
  el.height = Math.max(1, Math.round(viewH * dpr))

  cols = Math.max(1, Math.ceil(viewW / GAP) + 1)
  rows = Math.max(1, Math.ceil(viewH / GAP) + 1)

  // 场画布 = 点阵分辨率:1 像素对应 1 个点,是整个管线省 CPU 的关键
  if (!field) field = document.createElement('canvas')
  field.width = cols
  field.height = rows
  fieldCtx = field.getContext('2d', { willReadFrequently: true })

  readColor()
  mask = createLayer(cols, rows)
  maskCtx = mask.getContext('2d')
  maskImage = maskCtx?.createImageData(cols, rows) ?? null
  rebuildDotLayers()
}

/** 形状内的线性渐变:沿 gradAngle 从全亮衰减到 40%,让点亮有方向感 */
function shapeFillGradient(s: ShapeDef, alpha: number, unit: number) {
  if (!fieldCtx) return '#fff'
  const r = s.radius * s.scale * unit
  const ang = s.gradAngle
  const dx = Math.cos(ang) * r
  const dy = Math.sin(ang) * r
  const hi = Math.min(1, alpha)
  const lo = Math.min(1, alpha * 0.4)
  const grad = fieldCtx.createLinearGradient(-dx, -dy, dx, dy)
  grad.addColorStop(0, `rgba(255,255,255,${hi})`)
  grad.addColorStop(1, `rgba(255,255,255,${lo})`)
  return grad
}

function drawStar(r: number, points = 8) {
  if (!fieldCtx) return
  const inner = r * 0.42
  fieldCtx.beginPath()
  for (let i = 0; i < points * 2; i++) {
    const rad = (i * Math.PI) / points - Math.PI / 2
    const dist = i % 2 === 0 ? r : inner
    const x = Math.cos(rad) * dist
    const y = Math.sin(rad) * dist
    if (i === 0) fieldCtx.moveTo(x, y)
    else fieldCtx.lineTo(x, y)
  }
  fieldCtx.closePath()
}

function drawTri(r: number) {
  if (!fieldCtx) return
  fieldCtx.beginPath()
  fieldCtx.moveTo(0, -r)
  fieldCtx.lineTo(r * 0.92, r * 0.78)
  fieldCtx.lineTo(-r * 0.92, r * 0.78)
  fieldCtx.closePath()
}

function drawShape(s: ShapeDef, frame: number) {
  if (!fieldCtx) return
  const unit = Math.min(cols, rows)
  const nx = sampleTrack(s.pos, frame, 0)
  const ny = sampleTrack(s.pos, frame, 1)
  const rotDeg = sampleTrack(s.rot, frame, 0) + s.localRot
  const [cx, cy] = normToPx(nx, ny, cols, rows)
  const r = s.radius * s.scale * unit
  const alpha = isDark ? Math.max(s.opacity, 0.88) : 1

  fieldCtx.save()
  fieldCtx.translate(cx, cy)
  fieldCtx.rotate(rotDeg * (Math.PI / 180))
  fieldCtx.fillStyle = shapeFillGradient(s, alpha, unit)
  fieldCtx.strokeStyle = shapeFillGradient(s, alpha, unit)

  switch (s.kind) {
    case 'circle':
    case 'circle2': {
      fieldCtx.beginPath()
      fieldCtx.arc(0, 0, r, 0, Math.PI * 2)
      fieldCtx.fill()
      break
    }
    case 'ring': {
      const sw = (s.strokeW ?? s.radius * 0.28) * unit
      fieldCtx.lineWidth = sw
      fieldCtx.beginPath()
      fieldCtx.arc(0, 0, r, 0, Math.PI * 2)
      fieldCtx.stroke()
      break
    }
    case 'rect': {
      const asp = s.aspect ?? 1.5
      const w = r * asp
      const h = r
      fieldCtx.fillRect(-w / 2, -h / 2, w, h)
      break
    }
    case 'star':
      drawStar(r)
      fieldCtx.fill()
      break
    case 'tri':
      drawTri(r)
      fieldCtx.fill()
      break
  }
  fieldCtx.restore()
}

function drawField(frame: number) {
  if (!fieldCtx || !field) return
  fieldCtx.clearRect(0, 0, cols, rows)
  // 形状构图仍按主题分(亮暗两套编排不同);颜色不分——统一无彩,
  // 场里只写 alpha 覆盖率,着色在主画布用 dotColor 完成
  const theme = isDark ? THEMES.dark : THEMES.light
  theme.shapes.forEach(s => drawShape(s, frame))
}

function scheduleFrame() {
  cancelAnimationFrame(raf)
  if (!pageVisible || reduceMotion) return
  raf = requestAnimationFrame(render)
}

function render(now: number) {
  if (
    !ctx || !fieldCtx || !field || !canvasEl.value
    || !dotLayer || !mask || !maskCtx || !maskImage
  ) return
  // 30fps 帧率闸:跳过的帧只重新排队,不推进时间轴
  if (now - lastFrameAt < FRAME_MS) {
    scheduleFrame()
    return
  }
  lastFrameAt = now
  if (!lastNow) lastNow = now
  const dt = Math.min(0.05, (now - lastNow) / 1000)
  lastNow = now
  if (!reduceMotion) animTime = (animTime + dt * MOTION_RATE) % STAGE_DUR
  const frame = (animTime * STAGE_FR) % STAGE_OP

  drawField(frame)
  const fieldData = fieldCtx.getImageData(0, 0, cols, rows).data
  const maskData = maskImage.data
  const gain = isDark ? GAIN_DARK : GAIN_LIGHT
  for (let pixel = 0; pixel < cols * rows; pixel++) {
    const offset = pixel * 4
    const raw = fieldData[offset + 3]! / 255
    const level = raw <= 0.04 ? 0 : Math.min(LEVELS, Math.round(raw * LEVELS))
    maskData[offset + 3] = Math.round((level / LEVELS) * gain * 255)
  }
  maskCtx.putImageData(maskImage, 0, 0)

  ctx.setTransform(1, 0, 0, 1, 0, 0)
  ctx.globalCompositeOperation = 'source-over'
  ctx.clearRect(0, 0, canvasEl.value.width, canvasEl.value.height)
  ctx.drawImage(dotLayer, 0, 0)
  ctx.globalCompositeOperation = 'destination-in'
  ctx.imageSmoothingEnabled = false
  ctx.drawImage(
    mask,
    0,
    0,
    cols,
    rows,
    -GAP * dpr / 2,
    -GAP * dpr / 2,
    cols * GAP * dpr,
    rows * GAP * dpr,
  )
  ctx.globalCompositeOperation = 'source-over'
  scheduleFrame()
}

let resizeObserver: ResizeObserver | null = null
let themeObserver: MutationObserver | null = null
let mediaQuery: MediaQueryList | null = null

/** reduce-motion 下仍画一帧静态点阵(含当前形状),只是不再推进 */
function renderOnce() {
  cancelAnimationFrame(raf)
  raf = requestAnimationFrame(render)
}

function onReduceMotionChange() {
  reduceMotion = !!mediaQuery?.matches
  lastNow = 0
  lastFrameAt = 0
  if (reduceMotion) {
    renderOnce()
    return
  }
  scheduleFrame()
}

function onVisibilityChange() {
  pageVisible = !document.hidden
  lastNow = 0
  lastFrameAt = 0
  if (!pageVisible) {
    cancelAnimationFrame(raf)
    return
  }
  if (!reduceMotion) scheduleFrame()
}

onMounted(() => {
  const el = canvasEl.value
  if (!el) return
  ctx = el.getContext('2d')
  mediaQuery = window.matchMedia('(prefers-reduced-motion: reduce)')
  reduceMotion = mediaQuery.matches
  mediaQuery.addEventListener('change', onReduceMotionChange)
  document.addEventListener('visibilitychange', onVisibilityChange)
  resize()
  resizeObserver = new ResizeObserver(() => {
    resize()
    if (reduceMotion) renderOnce()
  })
  resizeObserver.observe(el)
  themeObserver = new MutationObserver(() => {
    readColor()
    rebuildDotLayers()
    if (reduceMotion) renderOnce()
  })
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
  raf = requestAnimationFrame(render)
})

onBeforeUnmount(() => {
  cancelAnimationFrame(raf)
  resizeObserver?.disconnect()
  themeObserver?.disconnect()
  mediaQuery?.removeEventListener('change', onReduceMotionChange)
  document.removeEventListener('visibilitychange', onVisibilityChange)
})
</script>
