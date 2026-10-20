// @vitest-environment jsdom

import { createApp, h, nextTick, type VNode } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

const uiStubs = vi.hoisted(() => {
  const element = (tag: string) => ({
    inheritAttrs: false,
    setup(_: unknown, context: { attrs: Record<string, unknown>; slots: { default?: () => VNode[] } }) {
      return () => h(tag, context.attrs, context.slots.default?.())
    },
  })
  return {
    Item: {
      inheritAttrs: false,
      props: { as: { type: [String, Object], default: 'div' }, size: String },
      setup(props: { as: string | object }, context: { attrs: Record<string, unknown>; slots: { default?: () => VNode[] } }) {
        return () => h(props.as, context.attrs, context.slots.default?.())
      },
    },
    ItemGroup: element('div'),
    Kbd: element('kbd'),
    Skeleton: element('span'),
  }
})

// Native elements keep the assertions about rows, focus and events explicit.
vi.mock('@felinic/ui', () => uiStubs)
vi.mock('lucide-vue-next', () => ({ ChevronRight: { render: () => h('svg', { 'data-chevron': '' }) } }))
vi.mock('vue-router', () => ({
  RouterLink: {
    props: { to: { type: [String, Object], required: true } },
    setup(props: { to: unknown }, context: { slots: { default?: () => VNode[] } }) {
      return () => h('a', { 'data-to': JSON.stringify(props.to) }, context.slots.default?.())
    },
  },
}))

import StarterPromptList, { type StarterPromptNotice, type StarterPromptRow } from './starter-prompt-list.vue'

const rows: StarterPromptRow[] = [
  { key: 'a', title: 'Shrine route', prompt: 'Plan a shrine route' },
  { key: 'b', title: 'New releases', prompt: 'List this week\'s releases' },
  { key: 'c', title: 'Version events', prompt: 'Remind me of the next version events' },
]

describe('StarterPromptList', () => {
  let app: ReturnType<typeof createApp> | undefined
  let root: HTMLDivElement | undefined

  afterEach(() => {
    app?.unmount()
    root?.remove()
    app = undefined
    root = undefined
  })

  function mount(list: StarterPromptRow[] | null, enterHint = true, notice?: StarterPromptNotice) {
    const select = vi.fn()
    const preview = vi.fn()
    root = document.createElement('div')
    document.body.append(root)
    app = createApp({ render: () => h(StarterPromptList, { rows: list, enterHint, notice, onSelect: select, onPreview: preview }) })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    const buttons = () => [...root!.querySelectorAll<HTMLButtonElement>('button[data-starter-prompt]')]
    return { select, preview, buttons }
  }

  it('renders three busy skeleton rows while the set is loading', () => {
    mount(null)
    expect(root!.querySelector('[aria-busy="true"]')).not.toBeNull()
    expect(root!.querySelectorAll('[data-starter-prompt]')).toHaveLength(3)
    expect(root!.querySelectorAll('button')).toHaveLength(0)
  })

  it('previews a row on hover and focus, and clears it on leave and blur', async () => {
    const { preview, buttons } = mount(rows)
    const [first] = buttons()
    first!.dispatchEvent(new MouseEvent('mouseenter'))
    first!.dispatchEvent(new MouseEvent('mouseleave'))
    first!.dispatchEvent(new FocusEvent('focus'))
    first!.dispatchEvent(new FocusEvent('blur'))
    await nextTick()
    expect(preview.mock.calls).toEqual([['Plan a shrine route'], [null], ['Plan a shrine route'], [null]])
  })

  it('shows the use hint on every row while Enter would pick it', () => {
    const { buttons } = mount(rows)
    expect(buttons().every(row => row.querySelector('[data-starter-prompt-hint]'))).toBe(true)
  })

  it('hides the use hint when Enter would not pick a row', () => {
    const { buttons } = mount(rows, false)
    expect(buttons()[0]!.querySelector('[data-starter-prompt-hint]')).toBeNull()
  })

  it('selects the clicked row', () => {
    const { select, buttons } = mount(rows)
    buttons()[1]!.click()
    expect(select).toHaveBeenCalledWith(rows[1])
  })

  it('moves focus with the arrow keys, Home and End, wrapping at the ends', () => {
    const { buttons } = mount(rows)
    const press = (key: string) => document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
    buttons()[0]!.focus()
    press('ArrowDown')
    expect(document.activeElement).toBe(buttons()[1])
    press('End')
    expect(document.activeElement).toBe(buttons()[2])
    press('ArrowDown')
    expect(document.activeElement).toBe(buttons()[0])
    press('ArrowUp')
    expect(document.activeElement).toBe(buttons()[2])
    press('Home')
    expect(document.activeElement).toBe(buttons()[0])
  })
  it('renders a notice with a destination as a link row in place of the rows', () => {
    mount([], true, { title: 'Set a title model', description: 'Pick one on your profile.', to: { name: 'profile' } })
    const link = root!.querySelector<HTMLAnchorElement>('a[data-starter-prompt-notice]')
    expect(link?.dataset.to).toBe(JSON.stringify({ name: 'profile' }))
    expect(link?.textContent).toContain('Set a title model')
    expect(link?.textContent).toContain('Pick one on your profile.')
    expect(link?.querySelector('[data-chevron]')).not.toBeNull()
    expect(root!.querySelectorAll('button')).toHaveLength(0)
  })

  it('renders a notice without a destination as plain text', () => {
    mount([], true, { title: 'No title model', description: 'Ask the owner to set one.' })
    const notice = root!.querySelector('[data-starter-prompt-notice]')
    expect(notice?.tagName).toBe('DIV')
    expect(notice?.textContent).toContain('Ask the owner to set one.')
    expect(root!.querySelector('a')).toBeNull()
    expect(root!.querySelector('[data-chevron]')).toBeNull()
  })

  it('shows the notice even while a set would be loading', () => {
    mount(null, true, { title: 'No title model', description: 'Ask the owner to set one.' })
    expect(root!.querySelector('[data-starter-prompt-notice]')).not.toBeNull()
    expect(root!.querySelector('[aria-busy="true"]')).toBeNull()
  })
})
