// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */

import { createApp, defineComponent, h, nextTick } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ChatAssistantTurn, ChatUserTurn } from '@/store/chat/types'
import { buildTrajectoryRows } from '../../composables/trajectory-model'
import TrajectoryLedger from './trajectory-ledger.vue'
import TrajectoryStats from './trajectory-stats.vue'
import TrajectoryOverview from './trajectory-overview.vue'

vi.mock('@felinic/ui', () => ({
  Spinner: defineComponent({ setup: () => () => h('span', { 'data-testid': 'spinner' }) }),
}))

const mounted: { app: ReturnType<typeof createApp>, root: HTMLDivElement }[] = []

function mount(component: unknown, props: Record<string, unknown>) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const i18n = createI18n({
    legacy: false,
    locale: 'en',
    messages: {
      en: {
        chat: {
          trajectory: {
            turn: 'Turn {n}', step: 'Step {n}', steering: 'steering', prepared: 'prepared', systemPreview: '{tokens} tokens in context',
            kindSystem: 'SYSTEM', kindUser: 'USER', kindContext: 'CONTEXT', kindAssistant: 'ASSISTANT', kindReasoning: 'REASONING', kindTool: 'TOOL', kindError: 'ERROR', kindNotice: 'NOTICE',
            laneInput: 'Input', laneModel: 'Model', laneTools: 'Tools', timelineEmpty: 'No timing',
            statsTurns: '{n} turns', statsSteps: '{n} steps', statsLlm: 'LLM {s}', statsTools: 'Tools {s}', statsTtft: 'TTFT avg {s}', statsTokPerSec: '{n} tok/s', statsCacheHit: 'Cache hit {p}%', statsInput: 'Input {n} tok', statsOutput: 'Output {n} tok', statsScope: 'loaded turns only',
          },
        },
      },
    },
  })
  const app = createApp(defineComponent({ setup: () => () => h(component as never, props) }))
  app.use(i18n)
  app.mount(root)
  mounted.push({ app, root })
  return root
}

afterEach(() => {
  for (const { app, root } of mounted.splice(0)) {
    app.unmount()
    root.remove()
  }
})

function user(id: string, text: string, turnId: string): ChatUserTurn {
  return { id, role: 'user', text, attachments: [], timestamp: '2026-09-03T00:00:00.000Z', streaming: false, isSelf: true, turnId, turnPosition: Number(turnId.slice(-1)) }
}

function assistant(id: string, turnId: string, blocks: number): ChatAssistantTurn {
  return {
    id,
    role: 'assistant',
    turnId,
    turnPosition: Number(turnId.slice(-1)),
    timestamp: '2026-09-03T00:00:01.000Z',
    streaming: false,
    messages: Array.from({ length: blocks }, (_, index) => ({ id: index, type: 'text' as const, content: `block ${index}` })),
    stepTraces: [{ first_message_id: 0, step_index: 0, started_at_ms: 1_000, first_token_at_ms: 1_100, ended_at_ms: 1_600, usage: { input_tokens: 10, output_tokens: 5 } }],
  }
}

describe('trajectory ledger', () => {
  it('mounts only the rows inside the viewport window and emits the selected row', async () => {
    const rows = buildTrajectoryRows([user('u1', 'hello', 'turn-1'), assistant('a1', 'turn-1', 400)], new Map())
    expect(rows).toHaveLength(401)
    const onSelect = vi.fn()
    const root = mount(TrajectoryLedger, { rows, selectedKey: rows[1]!.key, onSelect })
    await nextTick()
    const mountedRows = root.querySelectorAll('[data-testid^="trajectory-row-"]')
    expect(mountedRows.length).toBeGreaterThan(0)
    expect(mountedRows.length).toBeLessThan(60)
    expect(root.querySelector('[data-testid="trajectory-row-user"]')?.textContent).toContain('Turn 1')
    expect(root.querySelector('[data-ui-selected]')?.textContent).toContain('block 0')
    ;(root.querySelector('[data-testid="trajectory-row-user"]') as HTMLElement).click()
    expect(onSelect).toHaveBeenCalledWith(rows[0]!.key)
  })

  it('labels injected context rows and tool rows with their arguments and result', async () => {
    const turn = assistant('a1', 'turn-1', 0)
    turn.messages = [{
      id: 0, type: 'tool', name: 'exec', toolName: 'exec', tool_call_id: 'c', toolCallId: 'c', input: { cmd: 'ls' }, output: 'ok', result: 'ok', running: false, done: true,
      execution_timing: { started_at_ms: 1_000, ended_at_ms: 1_850 },
    }]
    const injected = user('u2', 'hurry', 'turn-1')
    injected.contextInjection = { kind: 'steering' }
    const rows = buildTrajectoryRows([turn, injected], new Map())
    const root = mount(TrajectoryLedger, { rows, selectedKey: null })
    await nextTick()
    const tool = root.querySelector('[data-testid="trajectory-row-tool"]')!
    expect(tool.textContent).toContain('exec')
    expect(tool.textContent).toContain('{"cmd":"ls"}')
    expect(tool.textContent).toContain('ok')
    expect(tool.textContent).toContain('850ms')
    expect(root.querySelector('[data-testid="trajectory-row-context"]')?.textContent).toContain('steering')
  })
})

describe('trajectory stats', () => {
  it('renders sampled groups only', () => {
    const root = mount(TrajectoryStats, { stats: { turns: 2, steps: 3, toolCalls: 1, llmMs: 2_500, toolMs: 0, ttftAvgMs: null, decodeMs: 0, decodeTokens: 0, inputTokens: 0, cachedInputTokens: 0, outputTokens: 0 } })
    const text = root.querySelector('[data-testid="trajectory-stats"]')!.textContent!
    expect(text).toContain('2 turns')
    expect(text).toContain('3 steps')
    expect(text).toContain('LLM 2.5s')
    expect(text).not.toContain('Tools')
    expect(text).not.toContain('TTFT')
    expect(text).toContain('loaded turns only')
  })
})

describe('trajectory overview', () => {
  it('draws one bar per span with proportional geometry', () => {
    const root = mount(TrajectoryOverview, {
      mode: 'duration',
      timeline: {
        start: 0,
        end: 4_000,
        steps: 1,
        spans: [
          { lane: 'model', key: 'model:0', start: 0, end: 2_000, ttftEnd: 500, label: 'stop', tokens: 5, cachedTokens: 0, stepIndex: 0 },
          { lane: 'input', key: 'input:0', start: 0, end: 2_000, ttftEnd: null, label: '', tokens: 100, cachedTokens: 0, stepIndex: 0 },
          { lane: 'tools', key: 'tool:1', start: 2_000, end: 4_000, ttftEnd: null, label: 'exec', tokens: 0, cachedTokens: 0, stepIndex: 0 },
        ],
      },
    })
    const model = root.querySelector('[data-testid="trajectory-bar-model:0"]') as HTMLElement
    expect(model.style.width).toBe('50%')
    expect(model.style.left).toBe('0%')
    const tool = root.querySelector('[data-testid="trajectory-bar-tool:1"]') as HTMLElement
    expect(tool.style.left).toBe('50%')
    expect(tool.title).toContain('exec')
    expect(root.querySelectorAll('[data-testid^="trajectory-bar-"]')).toHaveLength(3)
  })

  it('states when a turn carries no timing', () => {
    const root = mount(TrajectoryOverview, { mode: 'duration', timeline: null })
    expect(root.textContent).toContain('No timing')
  })
})
