#!/usr/bin/env node
// Regenerate the home-screen icons referenced by public/manifest.webmanifest and
// index.html from public/logo.svg.
//
// Outputs (all in public/):
//   apple-touch-icon.png  180x180  iOS "Add to Home Screen"
//   pwa-192.png           192x192  manifest icon, purpose "any"
//   pwa-512.png           512x512  manifest icon, purpose "any"
//   pwa-maskable-512.png  512x512  manifest icon, purpose "maskable"
//
// Run: pnpm --filter @memohai/web icons
//
// Every icon is opaque: iOS fills transparent pixels with black, and Android
// launchers draw "any" icons on their own plate. The logo sits on the same white
// as the desktop app icon. Maskable icons get a smaller mark because launchers
// may crop anything outside the central 80% safe-zone circle.

import { readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import sharp from 'sharp'

const __dirname = dirname(fileURLToPath(import.meta.url))
const PUBLIC_DIR = resolve(__dirname, '../public')
const BACKGROUND = '#ffffff'

const ICONS = [
  { file: 'apple-touch-icon.png', size: 180, markWidth: 0.68 },
  { file: 'pwa-192.png', size: 192, markWidth: 0.68 },
  { file: 'pwa-512.png', size: 512, markWidth: 0.68 },
  { file: 'pwa-maskable-512.png', size: 512, markWidth: 0.54 },
]

const svg = await readFile(resolve(PUBLIC_DIR, 'logo.svg'))

for (const { file, size, markWidth } of ICONS) {
  const width = Math.round(size * markWidth)
  const mark = await sharp(svg, { density: 600 }).resize({ width }).png().toBuffer()
  await sharp({ create: { width: size, height: size, channels: 4, background: BACKGROUND } })
    .composite([{ input: mark, gravity: 'center' }])
    .flatten({ background: BACKGROUND })
    .png({ compressionLevel: 9 })
    .toFile(resolve(PUBLIC_DIR, file))
  console.log(`wrote public/${file}`)
}
