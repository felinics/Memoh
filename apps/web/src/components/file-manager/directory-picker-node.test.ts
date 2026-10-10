// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, type App } from 'vue'
import { UserFacingError } from '@/utils/api-error'
import DirectoryPickerNode from './directory-picker-node.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

let app: App | null = null
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
})

async function mountFailing(error: unknown): Promise<HTMLElement> {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(() => h(DirectoryPickerNode, {
    path: '/work', name: 'work', depth: 0, selectedPath: '',
    listDirectory: () => Promise.reject(error),
    expandOnMount: true,
  }))
  app.mount(root)
  await vi.waitFor(() => expect(root.textContent).toContain('bots.folders.form.browseRetry'))
  return root
}

describe('directory picker node', () => {
  it('shows the text of a UserFacingError on the retry line', async () => {
    const root = await mountFailing(new UserFacingError('The computer is offline.'))
    expect(root.textContent).toContain('The computer is offline.')
  })

  it('shows local copy instead of the text of any other error', async () => {
    const root = await mountFailing(new Error('ECONNREFUSED 10.0.0.7:8443'))
    expect(root.textContent).toContain('bots.folders.form.browseFailed')
    expect(root.textContent).not.toContain('10.0.0.7')
  })
})
