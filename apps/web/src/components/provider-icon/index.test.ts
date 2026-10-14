// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref, shallowRef } from 'vue'

vi.mock('./icons.ts', () => ({ iconMap: {} }))
vi.mock('./preload', () => ({ providerIconSource: (url: string) => shallowRef(url.includes('/blocked') ? null : url) }))
import ProviderIcon from './index.vue'

let app: ReturnType<typeof createApp> | undefined
afterEach(() => { app?.unmount(); document.body.innerHTML = '' })

it('replaces a failed image with the caller fallback and recovers when its URL changes', async () => {
  const icon = ref('https://example.com/dead.svg')
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(() => h(ProviderIcon, { icon: icon.value, class: 'size-4' }, {
    default: () => h('svg', { 'data-fallback': '' }),
  }))
  app.mount(root)
  root.querySelector('img')!.dispatchEvent(new Event('error'))
  await nextTick()
  expect(root.querySelector('img')).toBeNull()
  expect(root.querySelector('span.size-4 > svg[data-fallback]')).not.toBeNull()
  icon.value = 'https://example.com/working.svg'
  await nextTick()
  expect(root.querySelector('img')!.getAttribute('src')).toBe(icon.value)
  expect(root.querySelector('[data-fallback]')).toBeNull()
})

it('shows the caller fallback without an image request for a native rejection', async () => {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(() => h(ProviderIcon, { icon: 'https://example.com/blocked', class: 'size-4' }, {
    default: () => h('svg', { 'data-fallback': '' }),
  }))
  app.mount(root)
  expect(root.querySelector('img')).toBeNull()
  expect(root.querySelector('span.size-4 > svg[data-fallback]')).not.toBeNull()
})

// An em size cannot go in the width attribute, which only takes pixels; it has to
// reach the image as a style or the SVG renders at its own size.
it('sizes an image by style, and leaves it to classes when no size is given', async () => {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(() => h('div', [
    h(ProviderIcon, { icon: 'https://example.com/em.svg', size: '1.5em' }),
    h(ProviderIcon, { icon: 'https://example.com/px.svg', size: 22 }),
    h(ProviderIcon, { icon: 'https://example.com/class.svg', class: 'size-4' }),
  ]))
  app.mount(root)
  const [em, px, byClass] = [...root.querySelectorAll('img')]
  expect(em!.style.width).toBe('1.5em')
  expect(em!.hasAttribute('width')).toBe(false)
  expect(px!.getAttribute('width')).toBe('22')
  expect(px!.style.height).toBe('22px')
  expect(byClass!.getAttribute('style')).toBeNull()
})

it('paints a monochrome logo in the text color through its shape', async () => {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(() => h(ProviderIcon, { icon: 'https://example.com/mono.svg', size: '1.5em', monochrome: true }))
  app.mount(root)
  expect(root.querySelector('img')).toBeNull()
  const mask = root.querySelector('span[role="img"]') as HTMLElement
  expect(mask.className).toContain('bg-current')
  expect(mask.style.width).toBe('1.5em')
  expect(mask.getAttribute('style')).toContain('mono.svg')
})

it('reads a unitless size string as pixels, so a masked logo keeps its size', async () => {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(() => h(ProviderIcon, { icon: 'https://example.com/mono.svg', size: '22', monochrome: true }))
  app.mount(root)
  const mask = root.querySelector('span[role="img"]') as HTMLElement
  expect(mask.style.width).toBe('22px')
  expect(mask.style.height).toBe('22px')
})
