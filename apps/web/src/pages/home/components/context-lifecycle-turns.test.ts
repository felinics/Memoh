// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */

import { computed, createApp, defineComponent, h, inject, nextTick, provide } from 'vue'
import type { ComputedRef } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { HandlersContextLifecycleTurn } from '@memohai/sdk'
import ContextLifecycleTurns from './context-lifecycle-turns.vue'

// reka-ui resolves its own Vue copy under vitest, so its primitives cannot be
// mounted here; the collapsible is stubbed down to its open/closed contract.
vi.mock('@felinic/ui', () => {
  const openKey = Symbol('collapsible-open')
  return {
    Collapsible: defineComponent({
      name: 'CollapsibleStub',
      props: { open: { type: Boolean, default: false } },
      setup(props, { slots }) {
        provide(openKey, computed(() => props.open))
        return () => h('div', slots.default?.())
      },
    }),
    CollapsibleTrigger: defineComponent({
      name: 'CollapsibleTriggerStub',
      inheritAttrs: false,
      setup(_, { attrs, slots }) {
        return () => h('button', attrs, slots.default?.())
      },
    }),
    CollapsibleContent: defineComponent({
      name: 'CollapsibleContentStub',
      setup(_, { slots }) {
        const open = inject<ComputedRef<boolean>>(openKey)
        return () => (open?.value ? h('div', slots.default?.()) : null)
      },
    }),
  }
})

const mounted: { app: ReturnType<typeof createApp>, root: HTMLDivElement }[] = []

afterEach(() => {
  for (const item of mounted.splice(0)) {
    item.app.unmount()
    item.root.remove()
  }
})

async function mountTurns(turns: HandlersContextLifecycleTurn[]): Promise<HTMLDivElement> {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(ContextLifecycleTurns, { turns })
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
          lifecycle: {
            statusCompleted: 'Completed',
            statusFallback: 'Fallback',
            statusFailedBudget: 'Budget failed',
            statusFailedProvider: 'Provider failed',
            statusAborted: 'Aborted',
            composition: 'Composition',
            selection: 'Selection',
            selectedCount: '{n} selected',
            droppedCount: '{n} dropped',
            truncatedCount: '{n} truncated',
            dropReasons: 'Drop reasons',
            trust: 'Trust',
            trustSystem: 'System',
            trustWorkspace: 'Workspace',
            trustUser: 'User',
            trustExternal: 'External',
            unknown: 'Unknown',
            mutations: 'Mutations',
            steps: 'Steps',
            window: 'Window',
            stablePrefix: 'Stable prefix',
            cacheRead: 'Cache read',
            cacheWrite: 'Cache write',
            empty: 'No lifecycle data for this session yet',
          },
        },
      },
    },
  }))
  app.mount(root)
  mounted.push({ app, root })
  await nextTick()
  return root.firstElementChild as HTMLDivElement
}

function texts(root: HTMLElement, selector: string): string[] {
  return [...root.querySelectorAll(selector)].map(el => (el.textContent ?? '').trim())
}

const richTurn: HandlersContextLifecycleTurn = {
  run_id: 'run-a',
  created_at: '2026-08-31T10:00:00.000Z',
  status: 'fallback',
  snapshot: {
    model: 'claude-opus-4',
    breakdown: [
      { kind: 'system_prompt', token_estimate: 1000 },
      { kind: 'conversation_event', token_estimate: 3000 },
    ],
    budget_plan: { window: 10_000, output_reserve: 2000 },
    counts: { token_estimate: 4200 },
    selection: { selected: 12, dropped: 3 },
    selection_decisions: [
      { decision: 'dropped', reason: 'budget', token_estimate: 1500 },
      { decision: 'dropped', reason: 'budget', token_estimate: 500 },
      { decision: 'trimmed', reason: 'stale', token_estimate: 100 },
      { decision: 'selected', reason: 'kept', token_estimate: 900 },
    ],
    trust_breakdown: [
      { trust: 'system', token_estimate: 1000 },
      { trust: 'external', token_estimate: 250 },
    ],
    mutations: [{ kind: 'mid_task_prune', detail: 'pruned 2 tool outputs' }],
    steps: [
      { step_index: 0 },
      { step_index: 1, dropped: 2, truncated: 1, reselection_applied: true, reselection_outcome: 'reselected' },
    ],
    stable_prefix_token_estimate: 1200,
    cache_read_tokens: 800,
    cache_write_tokens: 0,
  },
}

const bareTurn: HandlersContextLifecycleTurn = {
  run_id: 'run-b',
  created_at: '2026-08-30T09:00:00.000Z',
  status: 'weird_status',
  snapshot: { model: 'gpt-5', counts: { token_estimate: 900 } },
}

describe('context-lifecycle-turns', () => {
  it('renders the empty message and no turn rows for an empty list', async () => {
    const root = await mountTurns([])

    expect(root.textContent).toContain('No lifecycle data for this session yet')
    expect(root.querySelectorAll('[data-testid="turn-row"]')).toHaveLength(0)
  })

  it('renders one row per turn with a known status label in its tone class', async () => {
    const root = await mountTurns([richTurn, bareTurn])
    const statuses = [...root.querySelectorAll<HTMLElement>('[data-testid="turn-status"]')]

    expect(root.querySelectorAll('[data-testid="turn-row"]')).toHaveLength(2)
    expect(statuses[0]?.textContent?.trim()).toBe('Fallback')
    expect(statuses[0]?.classList.contains('text-warning')).toBe(true)
  })

  it('falls back to the raw status text with a muted tone for an unknown status', async () => {
    const root = await mountTurns([richTurn, bareTurn])
    const statuses = [...root.querySelectorAll<HTMLElement>('[data-testid="turn-status"]')]

    expect(statuses[1]?.textContent?.trim()).toBe('weird_status')
    expect(statuses[1]?.classList.contains('text-muted-foreground')).toBe(true)
  })

  it('totals a turn from its composition, falling back to the manifest count', async () => {
    const root = await mountTurns([richTurn, bareTurn])

    expect(texts(root, '[data-testid="turn-total"]')).toEqual(['4.0K', '900'])
  })

  it('expands only the first turn by default', async () => {
    const root = await mountTurns([richTurn, bareTurn])

    expect(root.querySelectorAll('[data-testid="turn-detail"]')).toHaveLength(1)
    expect(root.textContent).toContain('claude-opus-4')
  })

  it('renders the composition bar for a turn that reports a breakdown', async () => {
    const root = await mountTurns([richTurn])

    expect(root.querySelector('.bg-accent-gray')).not.toBeNull()
    expect(root.querySelector('.bg-accent-orange')).not.toBeNull()
    expect(root.textContent).toContain('Composition')
    expect(root.textContent).toContain('System prompt')
    expect(root.textContent).toContain('Conversation')
  })

  it('omits the composition block for a turn with no breakdown', async () => {
    const root = await mountTurns([bareTurn])

    expect(root.textContent).not.toContain('Composition')
  })

  it('summarises selection counts and groups dropped decisions by reason', async () => {
    const root = await mountTurns([richTurn])

    expect(root.textContent).toContain('12 selected')
    expect(root.textContent).toContain('3 dropped')
    expect(texts(root, '[data-testid="drop-reason"]')).toEqual(['budget', 'stale'])
    expect(texts(root, '[data-testid="drop-reason-value"]')).toEqual(['2 · 2.0K', '1 · 100'])
  })

  it('labels trust levels and leaves an unknown level raw', async () => {
    const root = await mountTurns([{
      ...richTurn,
      snapshot: { ...richTurn.snapshot, trust_breakdown: [{ trust: 'system', token_estimate: 1000 }, { token_estimate: 5 }] },
    }])

    expect(texts(root, '[data-testid="trust-label"]')).toEqual(['System', 'Unknown'])
  })

  it('lists window, stable prefix and cache metrics, omitting zero values', async () => {
    const root = await mountTurns([richTurn])

    expect(texts(root, '[data-testid="metric-label"]')).toEqual(['Window', 'Stable prefix', 'Cache read'])
    expect(texts(root, '[data-testid="metric-value"]')).toEqual(['10.0K', '1.2K', '800'])
  })

  it('lists mutations by kind with their detail', async () => {
    const root = await mountTurns([richTurn])

    expect(texts(root, '[data-testid="mutation-kind"]')).toEqual(['mid_task_prune'])
    expect(texts(root, '[data-testid="mutation-detail"]')).toEqual(['pruned 2 tool outputs'])
  })

  it('lists steps when more than one ran, with drop counts and the reselection outcome', async () => {
    const root = await mountTurns([richTurn])

    expect(texts(root, '[data-testid="step-label"]')).toEqual(['#0', '#1'])
    expect(texts(root, '[data-testid="step-value"]')).toEqual(['', '2 dropped · 1 truncated · reselected'])
  })

  it('omits the steps block for a single clean step', async () => {
    const root = await mountTurns([{
      ...richTurn,
      snapshot: { ...richTurn.snapshot, steps: [{ step_index: 0 }] },
    }])

    expect(root.querySelectorAll('[data-testid="step-label"]')).toHaveLength(0)
    expect(root.textContent).not.toContain('Steps')
  })

  it('keeps the steps block for a single step that dropped fragments', async () => {
    const root = await mountTurns([{
      ...richTurn,
      snapshot: { ...richTurn.snapshot, steps: [{ step_index: 0, dropped: 4 }] },
    }])

    expect(texts(root, '[data-testid="step-value"]')).toEqual(['4 dropped'])
  })
})
