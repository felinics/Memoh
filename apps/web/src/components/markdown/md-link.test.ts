// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@felinic/ui'
import { createApp, h, nextTick, reactive } from 'vue'
import MarkdownRender from 'markstream-vue'
import { registerSharedMarkdownComponents } from './index'
import { clearSiteIconLookups } from '@/utils/site-icon'

const mocks = vi.hoisted(() => ({ siteIcon: vi.fn(async () => ({ data: { light: 'https://github.com/favicon.svg', dark: 'https://github.com/favicon-dark.svg' } })), openFile: vi.fn(() => true), openBrowserAt: vi.fn(() => true), error: vi.fn() }))
vi.mock('@memohai/sdk', () => ({ getSiteIcon: mocks.siteIcon }))
const settings = reactive({ resolvedColorMode: 'dark' })
vi.mock('@/store/settings', () => ({ useSettingsStore: () => settings }))
vi.mock('@/store/workspace-tabs', () => ({ useWorkspaceTabsStore: () => mocks }))
vi.mock('@felinic/ui', async (importOriginal) => ({
  ...await importOriginal<typeof import('@felinic/ui')>(), toast: { error: mocks.error },
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

let unmount: (() => void) | undefined
afterEach(() => { unmount?.(); settings.resolvedColorMode = 'dark'; document.body.innerHTML = ''; clearSiteIconLookups(); vi.clearAllMocks() })

async function render(content: string) {
  registerSharedMarkdownComponents('link-test')
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(TooltipProvider, {}, () => h(MarkdownRender, {
    content, customId: 'link-test', final: true, typewriter: false, smoothStreaming: false, showTooltips: false,
  })) })
  app.mount(host)
  unmount = () => app.unmount()
  await nextTick()
  await vi.waitFor(() => expect(host.querySelector('a')).not.toBeNull())
  return host
}

describe('chat Markdown delivery links', () => {
  it('preserves inline rich labels, shows icons and opens each target in its own surface', async () => {
    const host = await render('做好了，可以[**打开试玩**](http://localhost:8080/index.html)，也可以[查看源文件](/data/counter/index.html)或[查阅官网](https://example.com)。')
    const links = host.querySelectorAll('a')
    expect(links).toHaveLength(3)
    // Link previews must not fall back to the delayed native browser title.
    expect(links[0]!.getAttribute('title')).toBeNull()
    expect(host.querySelectorAll('svg')).toHaveLength(3)
    expect(links[0]!.querySelector('strong')?.textContent).toContain('打开试玩')
    links[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(mocks.openBrowserAt).toHaveBeenCalledWith('localhost:8080/index.html')
    links[1]!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(mocks.openFile).toHaveBeenCalledWith('/data/counter/index.html')
    expect(links[2]!.target).toBe('_blank')
    expect(links[2]!.rel).toContain('noopener')
  })
  it('uses a loaded site icon and falls back when the icon is unavailable', async () => {
    const host = await render('[站点](https://github.com/path?q=private) [预览](http://localhost:8080/)')
    await vi.waitFor(() => expect(host.querySelector('img')).not.toBeNull())
    // Only the origin leaves the browser; the shared path and query stay private.
    expect(mocks.siteIcon).toHaveBeenCalledWith({ query: { url: 'https://github.com' } })
    const image = host.querySelector('img')!
    expect(image.src).toBe('https://github.com/favicon-dark.svg')
    expect(image.getAttribute('referrerpolicy')).toBe('no-referrer')
    expect(host.querySelectorAll('img')).toHaveLength(1)
    image.dispatchEvent(new Event('load'))
    await nextTick()
    expect(image.classList.contains('invisible')).toBe(false)
    expect(host.querySelectorAll('svg')).toHaveLength(1)
    image.dispatchEvent(new Event('error'))
    await nextTick()
    expect(host.querySelector('img')).toBeNull()
  })
  it('looks up each site once and switches icons with the theme without refetching', async () => {
    const host = await render('[a](https://github.com/a) [b](https://github.com/b?x=1) [c](https://github.com)')
    await vi.waitFor(() => expect(host.querySelectorAll('img')).toHaveLength(3))
    expect(mocks.siteIcon).toHaveBeenCalledTimes(1)
    settings.resolvedColorMode = 'light'
    await vi.waitFor(() => expect(host.querySelector('img')?.src).toBe('https://github.com/favicon.svg'))
    expect(host.querySelector('img')?.style.colorScheme).toBe('light')
    expect(mocks.siteIcon).toHaveBeenCalledTimes(1)
  })
  it('asks again for a site whose lookup came back empty', async () => {
    mocks.siteIcon.mockResolvedValueOnce({ data: { light: '', dark: '' } })
    let host = await render('[a](https://github.com/a)')
    await vi.waitFor(() => expect(mocks.siteIcon).toHaveBeenCalledTimes(1))
    expect(host.querySelector('img')).toBeNull()
    unmount?.()
    host = await render('[a](https://github.com/a)')
    await vi.waitFor(() => expect(host.querySelector('img')).not.toBeNull())
    expect(mocks.siteIcon).toHaveBeenCalledTimes(2)
  })
  it('keeps modifier and middle clicks in the workspace instead of navigating to the host', async () => {
    const host = await render('[文件](/data/a%20b.md)')
    const anchor = host.querySelector('a')!
    for (const event of [new MouseEvent('click', { bubbles: true, cancelable: true, ctrlKey: true }), new MouseEvent('auxclick', { bubbles: true, cancelable: true, button: 1 })]) {
      anchor.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(true)
    }
    expect(mocks.openFile).toHaveBeenCalledWith('/data/a b.md')
  })
  it('reports an unavailable preview without falling back to the user localhost', async () => {
    const host = await render('[预览](http://localhost:8080/)')
    mocks.openBrowserAt.mockReturnValueOnce(false)
    const event = new MouseEvent('click', { bubbles: true, cancelable: true })
    host.querySelector('a')!.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(true)
    expect(mocks.error).toHaveBeenCalledWith('chat.workspaceLinkUnavailable')
  })
})
