// @vitest-environment jsdom

import { createApp } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, describe, expect, it } from 'vitest'
import type { ContextCategoryStat, ContextComposition } from '../composables/context-categories'
import ContextUsageBreakdown from './context-usage-breakdown.vue'

const mounted: { app: ReturnType<typeof createApp>, root: HTMLDivElement }[] = []

afterEach(() => {
  for (const item of mounted.splice(0)) {
    item.app.unmount()
    item.root.remove()
  }
})

function mountBreakdown(props: {
  composition: ContextComposition
  contextWindow: number | null
  outputReserve?: number | null
  autoCompactTokens?: number | null
}): HTMLDivElement {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(ContextUsageBreakdown, props)
  app.use(createI18n({
    legacy: false,
    locale: 'en',
    messages: {
      en: {
        chat: {
          contextBreakdown: {
            system: 'System prompt',
            rules: 'Workspace rules',
            tools: 'Tools',
            skills: 'Skills',
            memory: 'Memory',
            summary: 'Summary',
            conversation: 'Conversation',
            other: 'Other',
            reserve: 'Output reserve',
            free: 'Free space',
          },
          infoAutoCompactAt: 'Auto-compact threshold ~{tokens}',
        },
      },
    },
  }))
  app.mount(root)
  mounted.push({ app, root })
  return root.firstElementChild as HTMLDivElement
}

function segments(root: HTMLElement): HTMLElement[] {
  return [...(root.firstElementChild?.children ?? [])] as HTMLElement[]
}

function legendRows(root: HTMLElement): HTMLElement[] {
  return [...(root.children[1]?.children ?? [])] as HTMLElement[]
}

function nth(elements: HTMLElement[], index: number): HTMLElement {
  const element = elements[index]
  if (!element) throw new Error(`no element at index ${index}`)
  return element
}

function rowText(row: HTMLElement): string[] {
  return [...row.children].map(child => (child.textContent ?? '').trim()).filter(text => text.length > 0)
}

const categories: ContextCategoryStat[] = [
  { id: 'system', tokens: 1000, colorClass: 'bg-accent-gray' },
  { id: 'tools', tokens: 3000, colorClass: 'bg-accent-purple' },
]
const composition: ContextComposition = { categories, totalTokens: 4000 }

describe('context-usage-breakdown', () => {
  it('renders one bar segment per category with its color class and window-relative width', () => {
    const root = mountBreakdown({ composition, contextWindow: 10_000 })
    const bars = segments(root)

    expect(bars).toHaveLength(2)
    expect(nth(bars, 0).classList.contains('bg-accent-gray')).toBe(true)
    expect(nth(bars, 1).classList.contains('bg-accent-purple')).toBe(true)
    expect(nth(bars, 0).style.width).toBe('10%')
    expect(nth(bars, 1).style.width).toBe('30%')
  })

  it('falls back to totalTokens as the bar denominator when no context window is known', () => {
    const root = mountBreakdown({ composition, contextWindow: null })
    const bars = segments(root)

    expect(nth(bars, 0).style.width).toBe('25%')
    expect(nth(bars, 1).style.width).toBe('75%')
  })

  it('scales against totalTokens when the estimate overflows the context window', () => {
    const root = mountBreakdown({
      composition: { categories: [{ id: 'system', tokens: 6000, colorClass: 'bg-accent-gray' }], totalTokens: 6000 },
      contextWindow: 4000,
    })

    expect(nth(segments(root), 0).style.width).toBe('100%')
  })

  it('lists legend rows in category order with swatches and formatted counts', () => {
    const root = mountBreakdown({ composition, contextWindow: 10_000 })
    const rows = legendRows(root)

    expect(rows.slice(0, 2).map(rowText)).toEqual([
      ['System prompt', '1.0K'],
      ['Tools', '3.0K'],
    ])
    expect(nth(rows, 0).firstElementChild?.classList.contains('bg-accent-gray')).toBe(true)
    expect(nth(rows, 1).firstElementChild?.classList.contains('bg-accent-purple')).toBe(true)
  })

  it('appends a muted free-space row when a context window is known', () => {
    const root = mountBreakdown({ composition, contextWindow: 10_000 })
    const rows = legendRows(root)

    expect(rows).toHaveLength(3)
    expect(rowText(nth(rows, 2))).toEqual(['Free space', '6.0K'])
    expect(nth(rows, 2).firstElementChild?.classList.contains('bg-accent')).toBe(true)
    expect(nth(rows, 2).lastElementChild?.classList.contains('text-muted-foreground')).toBe(true)
  })

  it('clamps the free-space row at zero when the estimate exceeds the window', () => {
    const root = mountBreakdown({ composition: { categories, totalTokens: 12_000 }, contextWindow: 10_000 })

    expect(rowText(nth(legendRows(root), 2))).toEqual(['Free space', '0'])
  })

  it('omits the free-space row when no context window is known', () => {
    const root = mountBreakdown({ composition, contextWindow: null })
    const rows = legendRows(root)

    expect(rows).toHaveLength(2)
    expect(root.textContent).not.toContain('Free space')
  })

  it('pins the output reserve band to the end of the track behind a flexible free gap', () => {
    const root = mountBreakdown({ composition, contextWindow: 10_000, outputReserve: 2000 })
    const bars = segments(root)

    expect(bars).toHaveLength(4)
    expect(nth(bars, 2).classList.contains('flex-1')).toBe(true)
    expect(nth(bars, 3).classList.contains('bg-border')).toBe(true)
    expect(nth(bars, 3).style.width).toBe('20%')
  })

  it('grows the bar denominator so the reserve still fits when the estimate overflows the window', () => {
    const root = mountBreakdown({
      composition: { categories: [{ id: 'system', tokens: 6000, colorClass: 'bg-accent-gray' }], totalTokens: 6000 },
      contextWindow: 4000,
      outputReserve: 2000,
    })
    const bars = segments(root)

    expect(nth(bars, 0).style.width).toBe('75%')
    expect(nth(bars, 2).style.width).toBe('25%')
  })

  it('subtracts the reserve from the free-space row and lists it above', () => {
    const root = mountBreakdown({ composition, contextWindow: 10_000, outputReserve: 2000 })
    const rows = legendRows(root)

    expect(rows).toHaveLength(4)
    expect(rowText(nth(rows, 2))).toEqual(['Output reserve', '2.0K'])
    expect(nth(rows, 2).firstElementChild?.classList.contains('bg-border')).toBe(true)
    expect(nth(rows, 2).lastElementChild?.classList.contains('text-muted-foreground')).toBe(true)
    expect(rowText(nth(rows, 3))).toEqual(['Free space', '4.0K'])
  })

  it('omits the reserve band and row when no output reserve is reported', () => {
    const root = mountBreakdown({ composition, contextWindow: 10_000 })

    expect(segments(root)).toHaveLength(2)
    expect(legendRows(root)).toHaveLength(3)
    expect(root.textContent).not.toContain('Output reserve')
  })

  it('marks the auto-compact threshold against the window and captions it as a threshold', () => {
    const root = mountBreakdown({ composition, contextWindow: 10_000, autoCompactTokens: 8000 })
    const tick = root.querySelector<HTMLElement>('.w-px')

    expect(tick?.classList.contains('bg-muted-foreground')).toBe(true)
    expect(tick?.style.left).toBe('80%')
    expect(root.lastElementChild?.classList.contains('text-caption')).toBe(true)
    expect(root.lastElementChild?.textContent?.trim()).toBe('Auto-compact threshold ~8.0K')
  })

  it('clamps the auto-compact mark to the last visible pixel of the track', () => {
    const root = mountBreakdown({ composition, contextWindow: 10_000, autoCompactTokens: 20_000 })

    expect(root.querySelector<HTMLElement>('.w-px')?.style.left).toBe('calc(100% - 1px)')
  })

  it('drops the reserve band, reserve row and auto-compact mark when no context window is known', () => {
    const root = mountBreakdown({ composition, contextWindow: null, outputReserve: 2000, autoCompactTokens: 8000 })

    expect(segments(root)).toHaveLength(2)
    expect(nth(segments(root), 0).style.width).toBe('25%')
    expect(legendRows(root)).toHaveLength(2)
    expect(root.querySelector('.w-px')).toBeNull()
    expect(root.textContent).not.toContain('Output reserve')
    expect(root.textContent).not.toContain('Auto-compact')
  })
})
