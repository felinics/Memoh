import { afterEach, expect, it, vi } from 'vitest'

const decode = vi.fn<() => Promise<void>>()
const request = vi.fn<typeof fetch>()
class MockImage {
  src = ''
  decode = decode
}

function setup() {
  vi.stubGlobal('Image', MockImage)
  vi.stubGlobal('fetch', request)
  decode.mockResolvedValue(undefined)
  request.mockImplementation(async () => new Response('<svg xmlns="http://www.w3.org/2000/svg"/>', {
    headers: { 'Content-Type': 'image/svg+xml' },
  }))
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.resetModules()
  decode.mockReset()
  request.mockReset()
})

it('shares pending work and decoded bytes between preload and repeated mounts for any URL', async () => {
  setup()
  const { providerIconSource, preloadProviderIcons } = await import('./preload')
  const url = 'https://custom.example/artwork.svg'
  preloadProviderIcons([url, 'slack', undefined])
  const first = providerIconSource(url)
  expect(providerIconSource(url)).toBe(first)
  await vi.waitFor(() => expect(first.value).toMatch(/^data:image\/svg\+xml;base64,/))
  for (let i = 0; i < 20; i++) expect(providerIconSource(url).value).toBe(first.value)
  expect(request).toHaveBeenCalledTimes(1)
  expect(decode).toHaveBeenCalledTimes(1)
})

it('falls back to normal embedding on CORS failure and retries on a later mount', async () => {
  setup()
  request.mockRejectedValueOnce(new TypeError('Failed to fetch'))
  const { providerIconSource } = await import('./preload')
  const url = 'https://custom.example/no-cors.png'
  const first = providerIconSource(url)
  await vi.waitFor(() => expect(first.value).toBe(url))
  const second = providerIconSource(url)
  await vi.waitFor(() => expect(second.value).toMatch(/^data:/))
  expect(request).toHaveBeenCalledTimes(2)
})

it('does not publish an undecodable data source', async () => {
  setup()
  decode.mockRejectedValueOnce(new Error('Invalid artwork'))
  const { providerIconSource } = await import('./preload')
  const url = 'https://custom.example/broken.svg'
  const source = providerIconSource(url)
  await vi.waitFor(() => expect(source.value).toBe(url))
})

it('evicts old cache entries without invalidating sources held by mounted consumers', async () => {
  setup()
  const { providerIconSource } = await import('./preload')
  const first = providerIconSource('https://custom.example/first.svg')
  await vi.waitFor(() => expect(first.value).toMatch(/^data:/))
  const loaded = first.value
  for (let i = 0; i < 128; i++) providerIconSource(`https://custom.example/${i}.svg`)
  expect(first.value).toBe(loaded)
  expect(providerIconSource('https://custom.example/first.svg')).not.toBe(first)
})
