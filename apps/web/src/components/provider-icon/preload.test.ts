import { afterEach, expect, it, vi } from 'vitest'

const decode = vi.fn<() => Promise<void>>()
const sources: string[] = []
class MockImage {
  onerror?: () => void
  set src(value: string) { sources.push(value) }
  decode = decode
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.resetModules()
  decode.mockReset()
  sources.length = 0
})

it('warms remote artwork once and leaves bundled icons alone', async () => {
  vi.stubGlobal('Image', MockImage)
  decode.mockResolvedValue(undefined)
  const { preloadProviderIcons } = await import('./preload')
  preloadProviderIcons(['https://example.test/github.svg', 'slack', undefined])
  preloadProviderIcons(['https://example.test/github.svg'])
  expect(sources).toEqual(['https://example.test/github.svg'])
  expect(decode).toHaveBeenCalledTimes(1)
})

it('allows a failed decode to be retried', async () => {
  vi.stubGlobal('Image', MockImage)
  decode.mockRejectedValueOnce(new Error('offline')).mockResolvedValue(undefined)
  const { preloadProviderIcons } = await import('./preload')
  preloadProviderIcons(['https://example.test/notion.svg'])
  await Promise.resolve()
  preloadProviderIcons(['https://example.test/notion.svg'])
  expect(decode).toHaveBeenCalledTimes(2)
})
