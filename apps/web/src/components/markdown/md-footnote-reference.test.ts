// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, type App } from 'vue'
import MdFootnoteReference from './md-footnote-reference.vue'

let app: App | null = null
let root: HTMLDivElement | null = null

afterEach(() => {
  app?.unmount()
  app = null
  root?.remove()
  root = null
})

describe('MdFootnoteReference', () => {
  it('scrolls to definitions whose ids contain colons, dots, or leading digits', () => {
    root = document.createElement('div')
    const target = document.createElement('div')
    target.id = 'fnref--1:note.a'
    const scrollIntoView = vi.fn()
    target.scrollIntoView = scrollIntoView
    const host = document.createElement('div')
    root.append(host, target)
    document.body.append(root)

    app = createApp({
      render: () => h(MdFootnoteReference, {
        node: { type: 'footnote_reference', id: '1:note.a', raw: '[^1:note.a]' },
      }),
    })
    app.mount(host)

    const link = host.querySelector('a')!
    expect(() => link.click()).not.toThrow()
    expect(scrollIntoView).toHaveBeenCalledWith({ behavior: 'smooth' })
  })
})
