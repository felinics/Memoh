#!/usr/bin/env node
// Regenerate every desktop icon asset from apps/web/public/logo.svg.
//
// Outputs:
//   build/icon.icon         macOS 26 Icon Composer source package
//   build/icon.png          1024x1024 unified raster
//   build/icon.icns         multi-resolution macOS fallback bundle icon
//   build/icon.ico          multi-resolution Windows icon
//   resources/icon.png      512x512 runtime BrowserWindow.icon / dock.setIcon
//   resources/tray-icon.png transparent runtime system tray/menu bar icon
//
// Run: pnpm --filter @memohai/desktop icons
//
// The app icon keeps the existing brand mark on a clean white enclosure, then
// adds a restrained glass-like rim to the mark itself. The source logo remains
// unchanged for the web app.

import { execFile } from 'node:child_process'
import { existsSync } from 'node:fs'
import { mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

import pngToIco from 'png-to-ico'
import sharp from 'sharp'

const execFileAsync = promisify(execFile)

const __dirname = dirname(fileURLToPath(import.meta.url))
const ROOT = resolve(__dirname, '..')
const SRC_SVG = resolve(ROOT, '../web/public/logo.svg')
const BUILD_DIR = resolve(ROOT, 'build')
const RESOURCES_DIR = resolve(ROOT, 'resources')
const XCODE_APP_CANDIDATES = [
  '/Applications/Xcode_26.4.1.app',
  '/Applications/Xcode_26.4.app',
  '/Applications/Xcode_26.3.app',
  '/Applications/Xcode_26.2.app',
  '/Applications/Xcode_26.1.1.app',
  '/Applications/Xcode_26.1.app',
  '/Applications/Xcode_26.0.1.app',
  '/Applications/Xcode_26.0.app',
  '/Applications/Xcode.app',
]
const ICON_COMPOSER_TOOL_CANDIDATES = [
  process.env.ICON_COMPOSER_TOOL,
  ...XCODE_APP_CANDIDATES.map((xcodeApp) => resolve(
    xcodeApp,
    'Contents/Applications/Icon Composer.app/Contents/Executables/ictool',
  )),
].filter(Boolean)
const ICON_COMPOSER_TOOL = ICON_COMPOSER_TOOL_CANDIDATES.find((candidate) => existsSync(candidate))
  ?? ICON_COMPOSER_TOOL_CANDIDATES[ICON_COMPOSER_TOOL_CANDIDATES.length - 1]

const MASTER_SIZE = 1024
// The checked-in standard source is the Icon Composer `.icon` bundle. The
// raster fallback keeps the same measured macOS 26 geometry: an opaque 824px
// enclosure centered at 100,100 on a 1024px master, plus a subtle Liquid
// Glass-style shadow extending just outside the enclosure.
const ENCLOSURE_SIZE = 824
const ENCLOSURE_SHADOW_PADDING = 72
// The brand mark is rendered into a fixed box, not resized by eye. 72% keeps
// the mark readable while preserving the breathing room expected for a
// foreground symbol on a rounded white enclosure.
const LOGO_BOX_RATIO = 0.72
const SQUIRCLE_EXPONENT = 5
const SQUIRCLE_POINTS = 192
const ICON_COMPOSER_ASSET = 'logo.png'
const ICON_COMPOSER_FOREGROUND_WIDTH = 560
const ICON_COMPOSER_FOREGROUND_LEFT = 230
const ICON_COMPOSER_FOREGROUND_TOP = 245
// The Icon Composer layer is scaled after Apple applies the platform enclosure
// and Liquid Glass rendering. 1.26 makes the rendered foreground land in the
// same visual band as ChatGPT/Claude-style Dock icons while preserving the
// standard 824px enclosure.
const ICON_COMPOSER_MARK_SCALE = 1.26

const ICONSET_SIZES = [
  { name: 'icon_16x16.png', size: 16 },
  { name: 'icon_16x16@2x.png', size: 32 },
  { name: 'icon_32x32.png', size: 32 },
  { name: 'icon_32x32@2x.png', size: 64 },
  { name: 'icon_128x128.png', size: 128 },
  { name: 'icon_128x128@2x.png', size: 256 },
  { name: 'icon_256x256.png', size: 256 },
  { name: 'icon_256x256@2x.png', size: 512 },
  { name: 'icon_512x512.png', size: 512 },
  { name: 'icon_512x512@2x.png', size: 1024 },
]

const ICO_SIZES = [16, 24, 32, 48, 64, 128, 256]

function clampByte(value) {
  return Math.max(0, Math.min(255, Math.round(value)))
}

function superellipsePath(size) {
  const center = size / 2
  const radius = size / 2
  const points = []

  for (let index = 0; index < SQUIRCLE_POINTS; index += 1) {
    const theta = (Math.PI * 2 * index) / SQUIRCLE_POINTS
    const cos = Math.cos(theta)
    const sin = Math.sin(theta)
    const x = center + radius * Math.sign(cos) * Math.abs(cos) ** (2 / SQUIRCLE_EXPONENT)
    const y = center + radius * Math.sign(sin) * Math.abs(sin) ** (2 / SQUIRCLE_EXPONENT)
    points.push(`${x.toFixed(2)},${y.toFixed(2)}`)
  }

  return `M${points.join('L')}Z`
}

function renderBackgroundSvg(size) {
  const shape = superellipsePath(size)
  const edgeWidth = Math.max(1, size * 0.01)
  const hairlineWidth = Math.max(1, size * 0.003)

  return Buffer.from(`
    <svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="0 0 ${size} ${size}">
      <defs>
        <clipPath id="mask">
          <path d="${shape}"/>
        </clipPath>
        <linearGradient id="edge" x1="${size * 0.11}" y1="${size * 0.05}" x2="${size * 0.9}" y2="${size * 0.97}" gradientUnits="userSpaceOnUse">
          <stop offset="0" stop-color="#ffffff" stop-opacity="0.94"/>
          <stop offset="0.5" stop-color="#f2f3f6" stop-opacity="0.24"/>
          <stop offset="1" stop-color="#d8dbe2" stop-opacity="0.34"/>
        </linearGradient>
      </defs>
      <g clip-path="url(#mask)">
        <rect width="${size}" height="${size}" fill="#ffffff"/>
      </g>
      <path d="${shape}" fill="none" stroke="url(#edge)" stroke-width="${edgeWidth}"/>
      <path d="${shape}" fill="none" stroke="#ffffff" stroke-width="${hairlineWidth}" stroke-opacity="0.9"/>
    </svg>
  `)
}

async function renderEnclosureShadow(size) {
  const shadowSize = size + ENCLOSURE_SHADOW_PADDING * 2
  const shape = superellipsePath(size)
  const shadowSvg = Buffer.from(`
    <svg xmlns="http://www.w3.org/2000/svg" width="${shadowSize}" height="${shadowSize}" viewBox="0 0 ${shadowSize} ${shadowSize}">
      <g transform="translate(${ENCLOSURE_SHADOW_PADDING} ${ENCLOSURE_SHADOW_PADDING})">
        <path d="${shape}" fill="#252a32" fill-opacity="0.12"/>
      </g>
    </svg>
  `)

  const halo = await sharp(shadowSvg).blur(size * 0.022).png().toBuffer()
  const ambient = await sharp(shadowSvg).blur(size * 0.02).png().toBuffer()
  const contact = await sharp(shadowSvg).blur(size * 0.007).png().toBuffer()

  return sharp({
    create: {
      width: shadowSize,
      height: shadowSize,
      channels: 4,
      background: { r: 0, g: 0, b: 0, alpha: 0 },
    },
  })
    .composite([
      { input: halo, left: 0, top: Math.round(size * 0.01), opacity: 0.33 },
      { input: ambient, left: 0, top: Math.round(size * 0.012), opacity: 0.32 },
      { input: contact, left: 0, top: Math.round(size * 0.008), opacity: 0.12 },
    ])
    .png()
    .toBuffer()
}

async function renderMarkEdgeLayers(mark) {
  const { data, info } = await sharp(mark)
    .ensureAlpha()
    .raw()
    .toBuffer({ resolveWithObject: true })

  const { width, height } = info
  const highlight = Buffer.alloc(width * height * 4)
  const shade = Buffer.alloc(width * height * 4)

  const alphaAt = (x, y) => {
    if (x < 0 || y < 0 || x >= width || y >= height) {
      return 0
    }

    return data[(y * width + x) * 4 + 3]
  }

  for (let y = 0; y < height; y += 1) {
    for (let x = 0; x < width; x += 1) {
      const index = (y * width + x) * 4
      const alpha = data[index + 3]

      if (alpha < 24) {
        continue
      }

      const topLeftExposure = Math.max(0, alpha - alphaAt(x - 4, y - 4))
      const bottomRightExposure = Math.max(0, alpha - alphaAt(x + 5, y + 5))
      const leftExposure = Math.max(0, alpha - alphaAt(x - 4, y))
      const topExposure = Math.max(0, alpha - alphaAt(x, y - 4))
      const rightExposure = Math.max(0, alpha - alphaAt(x + 4, y))
      const bottomExposure = Math.max(0, alpha - alphaAt(x, y + 4))

      const highlightAlpha = clampByte(
        topLeftExposure * 0.24 + leftExposure * 0.07 + topExposure * 0.07,
      )
      const shadeAlpha = clampByte(
        bottomRightExposure * 0.12 + rightExposure * 0.04 + bottomExposure * 0.04,
      )

      if (highlightAlpha > 4) {
        highlight[index] = 255
        highlight[index + 1] = 255
        highlight[index + 2] = 255
        highlight[index + 3] = Math.min(52, highlightAlpha)
      }

      if (shadeAlpha > 3) {
        shade[index] = 62
        shade[index + 1] = 36
        shade[index + 2] = 185
        shade[index + 3] = Math.min(24, shadeAlpha)
      }
    }
  }

  const options = { raw: { width, height, channels: 4 } }

  return {
    highlight: await sharp(highlight, options).png().toBuffer(),
    shade: await sharp(shade, options).png().toBuffer(),
  }
}

async function renderMark(svg) {
  const markBoxSize = Math.round(ENCLOSURE_SIZE * LOGO_BOX_RATIO)

  return sharp(svg, { density: 600 })
    .resize(markBoxSize, markBoxSize, {
      fit: 'contain',
      background: { r: 0, g: 0, b: 0, alpha: 0 },
    })
    .png()
    .toBuffer()
}

async function renderIconComposerLayer(mark) {
  const markBoxSize = Math.round(ENCLOSURE_SIZE * LOGO_BOX_RATIO)
  const markPosition = Math.floor((MASTER_SIZE - markBoxSize) / 2)

  return sharp({
    create: {
      width: MASTER_SIZE,
      height: MASTER_SIZE,
      channels: 4,
      background: { r: 0, g: 0, b: 0, alpha: 0 },
    },
  })
    .composite([{ input: mark, left: markPosition, top: markPosition }])
    .png()
    .toBuffer()
}

async function renderTrayIcon(svg) {
  const size = 64
  return sharp(svg, { density: 600 })
    .resize(size, size, {
      fit: 'contain',
      background: { r: 0, g: 0, b: 0, alpha: 0 },
    })
    .ensureAlpha()
    .png()
    .toBuffer()
}

async function writeIconComposerSource(markLayer) {
  const iconDir = resolve(BUILD_DIR, 'icon.icon')
  const assetsDir = resolve(iconDir, 'Assets')
  await rm(iconDir, { recursive: true, force: true })
  await mkdir(assetsDir, { recursive: true })

  await writeFile(resolve(assetsDir, ICON_COMPOSER_ASSET), markLayer)
  await writeFile(
    resolve(iconDir, 'icon.json'),
    `${JSON.stringify(
      {
        fill: 'system-light',
        groups: [
          {
            'blur-material': null,
            layers: [
              {
                'blend-mode-specializations': [
                  {
                    appearance: 'dark',
                    value: 'normal',
                  },
                ],
                glass: true,
                hidden: false,
                'image-name-specializations': [
                  {
                    value: ICON_COMPOSER_ASSET,
                  },
                ],
                name: 'Memoh mark',
                position: {
                  scale: ICON_COMPOSER_MARK_SCALE,
                  'translation-in-points': [0, 0],
                },
              },
            ],
            shadow: {
              kind: 'layer-color',
              opacity: 0.5,
            },
            specular: true,
            translucency: {
              enabled: true,
              value: 0.2,
            },
          },
        ],
        'supported-platforms': {
          circles: ['watchOS'],
          squares: 'shared',
        },
      },
      null,
      2,
    )}\n`,
  )
}

async function renderFallbackIcon(mark) {
  return renderRasterIcon(mark, null)
}

async function renderRasterIcon(mark, glassForeground) {
  const canvasSize = MASTER_SIZE
  const enclosureSize = ENCLOSURE_SIZE
  const enclosurePadding = Math.floor((canvasSize - enclosureSize) / 2)
  const markBoxSize = Math.round(enclosureSize * LOGO_BOX_RATIO)
  const markOffset = Math.floor((enclosureSize - markBoxSize) / 2)

  const top = enclosurePadding + markOffset
  const left = enclosurePadding + markOffset
  const background = await sharp(renderBackgroundSvg(enclosureSize)).png().toBuffer()
  const enclosureShadow = await renderEnclosureShadow(enclosureSize)
  const markEdges = await renderMarkEdgeLayers(mark)
  const foregroundLayers = glassForeground
    ? [
        {
          input: glassForeground,
          left: ICON_COMPOSER_FOREGROUND_LEFT,
          top: ICON_COMPOSER_FOREGROUND_TOP,
        },
      ]
    : [
        { input: mark, left, top },
        { input: markEdges.shade, left, top, blend: 'multiply' },
        { input: markEdges.highlight, left, top, blend: 'screen' },
      ]

  return sharp({
    create: {
      width: canvasSize,
      height: canvasSize,
      channels: 4,
      background: { r: 0, g: 0, b: 0, alpha: 0 },
    },
  })
    .composite([
      {
        input: enclosureShadow,
        left: enclosurePadding - ENCLOSURE_SHADOW_PADDING,
        top: enclosurePadding - ENCLOSURE_SHADOW_PADDING,
      },
      { input: background, left: enclosurePadding, top: enclosurePadding },
      ...foregroundLayers,
    ])
    .png()
    .toBuffer()
}

async function renderIconComposerForeground() {
  const iconDir = resolve(BUILD_DIR, 'icon.icon')
  const output = resolve(BUILD_DIR, 'icon-composer-export.png')

  await execFileAsync(ICON_COMPOSER_TOOL, [
    iconDir,
    '--export-image',
    '--output-file',
    output,
    '--platform',
    'macOS',
    '--rendition',
    'Default',
    '--width',
    String(MASTER_SIZE),
    '--height',
    String(MASTER_SIZE),
    '--scale',
    '1',
  ])

  try {
    const { data, info } = await sharp(output)
      .ensureAlpha()
      .raw()
      .toBuffer({ resolveWithObject: true })
    const { width, height } = info
    const foreground = Buffer.alloc(width * height * 4)
    const bounds = { minX: width, minY: height, maxX: -1, maxY: -1 }

    for (let y = 0; y < height; y += 1) {
      for (let x = 0; x < width; x += 1) {
        const index = (y * width + x) * 4
        const red = data[index]
        const green = data[index + 1]
        const blue = data[index + 2]
        const alpha = data[index + 3]
        const chroma = Math.max(red, green, blue) - Math.min(red, green, blue)
        const isPurpleGlass = alpha > 12
          && chroma > 12
          && blue > 120
          && red > 80
          && green < 205
          && blue > green + 18

        if (!isPurpleGlass) {
          continue
        }

        const glassAlpha = clampByte(Math.min(alpha, (chroma - 4) * 14))
        if (glassAlpha <= 0) {
          continue
        }

        foreground[index] = red
        foreground[index + 1] = green
        foreground[index + 2] = blue
        foreground[index + 3] = glassAlpha
        bounds.minX = Math.min(bounds.minX, x)
        bounds.minY = Math.min(bounds.minY, y)
        bounds.maxX = Math.max(bounds.maxX, x)
        bounds.maxY = Math.max(bounds.maxY, y)
      }
    }

    if (bounds.maxX < bounds.minX || bounds.maxY < bounds.minY) {
      throw new Error('Icon Composer export did not contain a foreground layer')
    }

    return sharp(foreground, { raw: { width, height, channels: 4 } })
      .extract({
        left: bounds.minX,
        top: bounds.minY,
        width: bounds.maxX - bounds.minX + 1,
        height: bounds.maxY - bounds.minY + 1,
      })
      .resize({ width: ICON_COMPOSER_FOREGROUND_WIDTH })
      .png()
      .toBuffer()
  } finally {
    await rm(output, { force: true })
  }
}

async function renderMaster(mark) {
  if (process.platform === 'darwin') {
    try {
      return await renderRasterIcon(mark, await renderIconComposerForeground())
    } catch (error) {
      console.warn(`  ! using fallback raster: ${ICON_COMPOSER_TOOL} could not export`)
      console.warn(`    ${error.message}`)
    }
  }

  return renderFallbackIcon(mark)
}

async function writeResized(master, size, outPath) {
  await sharp(master).resize(size, size, { fit: 'contain' }).png().toFile(outPath)
}

async function buildIcns(master) {
  const iconset = resolve(BUILD_DIR, 'icon.iconset')
  await rm(iconset, { recursive: true, force: true })
  await mkdir(iconset, { recursive: true })

  await Promise.all(
    ICONSET_SIZES.map(({ name, size }) =>
      writeResized(master, size, resolve(iconset, name)),
    ),
  )

  await execFileAsync('iconutil', [
    '-c', 'icns',
    iconset,
    '-o', resolve(BUILD_DIR, 'icon.icns'),
  ])
  await rm(iconset, { recursive: true, force: true })
}

async function buildIco(master) {
  const buffers = await Promise.all(
    ICO_SIZES.map(size =>
      sharp(master).resize(size, size, { fit: 'contain' }).png().toBuffer(),
    ),
  )
  const ico = await pngToIco(buffers)
  await writeFile(resolve(BUILD_DIR, 'icon.ico'), ico)
}

async function main() {
  await mkdir(BUILD_DIR, { recursive: true })
  await mkdir(RESOURCES_DIR, { recursive: true })

  console.log(`source: ${SRC_SVG}`)
  const svg = await readFile(SRC_SVG)
  const mark = await renderMark(svg)
  const markLayer = await renderIconComposerLayer(mark)

  await writeIconComposerSource(markLayer)
  const master = await renderMaster(mark)
  await Promise.all([
    sharp(master).png().toFile(resolve(BUILD_DIR, 'icon.png')),
    sharp(master).resize(512, 512, { fit: 'contain' }).png()
      .toFile(resolve(RESOURCES_DIR, 'icon.png')),
    writeFile(resolve(RESOURCES_DIR, 'tray-icon.png'), await renderTrayIcon(svg)),
  ])
  console.log('  -> build/icon.icon (macOS 26 Icon Composer)')
  console.log('  -> build/icon.png (1024)')
  console.log('  -> resources/icon.png (512)')
  console.log('  -> resources/tray-icon.png (transparent)')

  if (process.platform === 'darwin') {
    await buildIcns(master)
    console.log('  -> build/icon.icns (16…1024 + @2x)')
  } else {
    console.warn('  ! skipping icon.icns: iconutil only available on macOS')
  }

  await buildIco(master)
  console.log(`  -> build/icon.ico (${ICO_SIZES.join('/')})`)
}

main().catch(err => {
  console.error(err)
  process.exit(1)
})
