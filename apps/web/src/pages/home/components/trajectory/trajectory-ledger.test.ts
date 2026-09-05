// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */

import { createApp, defineComponent, h, nextTick } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { HandlersContextLifecycleTurn } from '@memohai/sdk'
import type { ChatAssistantTurn, ChatUserTurn } from '@/store/chat/types'
import { buildRowMap, buildTrajectoryRows, lifecycleByTurnId } from '../../composables/trajectory-model'
import { rowMapGeometry } from '../../composables/trajectory-view'
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
            turn: 'Turn {n}', step: 'Step {n}', steering: 'steering', prepared: 'prepared', systemPreview: '{fragments} fragments · {tokens} tok',
            contextFragments: '{fragments} fragments · {tokens} tok', contextHistory: '{messages} messages · {tokens} tok', contextHistoryCut: '{messages} messages · {tokens} tok · {dropped} dropped', contextToolDefs: '{n} tools · {tokens} tok',
            contextMemory: '{count} results · {state}', contextSelection: '{selected} selected · {dropped} dropped · {trimmed} trimmed', contextStep: 'dropped {dropped} · truncated {truncated} · {outcome}',
            contextKind: { workspace_instruction: 'workspace rules', tool_defs: 'tool definitions', conversation_event: 'history', selection: 'selection', step: 'reselection' },
            kindSystem: 'SYSTEM', kindUser: 'USER', kindContext: 'CONTEXT', kindAssistant: 'ASSISTANT', kindReasoning: 'REASONING', kindTool: 'TOOL', kindError: 'ERROR', kindNotice: 'NOTICE',
            laneInput: 'Input', laneModel: 'Model', laneTools: 'Tools', timelineEmpty: 'Nothing to map',
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

const lifecycle: HandlersContextLifecycleTurn = {
  run_id: 'run-1',
  turn_id: 'turn-1',
  created_at: '2026-09-03T00:00:01.000Z',
  snapshot: {
    version: 2,
    counts: { fragments: 4, token_estimate: 2_000 },
    breakdown: [
      { kind: 'system_prompt', fragments: 1, token_estimate: 1_300 },
      { kind: 'workspace_instruction', fragments: 1, token_estimate: 500 },
      { kind: 'conversation_event', fragments: 6, token_estimate: 84 },
    ],
    fragments: [
      { kind: 'system_prompt', slot: 'system', content_hash: 'c-sys', text_hash: 'h-sys', token_estimate: 1_300 },
      { kind: 'workspace_instruction', slot: 'system', content_hash: 'c-rules', text_hash: 'h-rules', token_estimate: 500 },
    ],
    tool_defs: [{ provider: 'workspace', name: 'exec', bytes: 800, token_estimate: 200 }],
    selection: { selected: 6, dropped: 2, drop_reasons: { budget: 2 } },
  },
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

  it('describes the system prompt and each context entry from the manifest', async () => {
    const rows = buildTrajectoryRows([user('u1', 'hello', 'turn-1'), assistant('a1', 'turn-1', 1)], lifecycleByTurnId([lifecycle]))
    const root = mount(TrajectoryLedger, { rows, selectedKey: null })
    await nextTick()
    expect(root.querySelector('[data-testid="trajectory-row-system"]')?.textContent).toContain('1 fragments · 1.3K tok')
    const contexts = [...root.querySelectorAll('[data-testid="trajectory-row-context"]')].map(node => node.textContent ?? '')
    expect(contexts[0]).toContain('workspace rules')
    expect(contexts[0]).toContain('1 fragments · 500 tok')
    expect(contexts[1]).toContain('history')
    expect(contexts[1]).toContain('6 messages · 84 tok · 2 dropped')
    expect(contexts[2]).toContain('tool definitions')
    expect(contexts[2]).toContain('1 tools · 200 tok')
    expect(contexts[3]).toContain('selection')
    expect(contexts[3]).toContain('6 selected · 2 dropped · 0 trimmed')
  })

  it('prefers the stored text of a fragment over its numbers', async () => {
    const rows = buildTrajectoryRows([user('u1', 'hello', 'turn-1'), assistant('a1', 'turn-1', 1)], lifecycleByTurnId([lifecycle]))
    const previews = { 'h-sys': { preview: 'You are Memoh, a careful agent.' }, 'h-rules': { preview: '# AGENTS.md\nRead this first.' } }
    const root = mount(TrajectoryLedger, { rows, selectedKey: null, previews })
    await nextTick()
    expect(root.querySelector('[data-testid="trajectory-row-system"]')?.textContent).toContain('You are Memoh, a careful agent.')
    const contexts = [...root.querySelectorAll('[data-testid="trajectory-row-context"]')].map(node => node.textContent ?? '')
    expect(contexts[0]).toContain('# AGENTS.md Read this first.')
    expect(contexts[1]).toContain('6 messages · 84 tok · 2 dropped')
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
  it('draws one bar per ledger record on its lane and focuses the row it maps', () => {
    const turn = assistant('a1', 'turn-1', 1)
    turn.messages = [
      { id: 0, type: 'text', content: 'answer' },
      { id: 1, type: 'tool', name: 'exec', toolName: 'exec', tool_call_id: 'c', toolCallId: 'c', input: {}, output: 'ok', result: 'ok', running: false, done: true, execution_timing: { started_at_ms: 1_700, ended_at_ms: 2_300 } },
    ]
    turn.stepTraces = [{ first_message_id: 0, last_message_id: 1, step_index: 0, started_at_ms: 1_000, first_token_at_ms: 1_150, ended_at_ms: 1_600, finish_reason: 'tool-calls' }]
    const rows = buildTrajectoryRows([user('u1', 'hello', 'turn-1'), turn], new Map())
    const bars = rowMapGeometry(buildRowMap(rows), 'duration')
    const onSelect = vi.fn()
    const root = mount(TrajectoryOverview, { bars, selectedKey: rows[0]!.key, onSelect })
    const drawn = root.querySelectorAll('[data-testid^="trajectory-bar-"]')
    expect(drawn).toHaveLength(3)
    const model = root.querySelector(`[data-testid="trajectory-bar-map:${rows[1]!.key}"]`) as HTMLElement
    expect(model.title).toContain('ASSISTANT')
    expect(model.title).toContain('tool-calls')
    expect(model.title).toContain('600ms')
    expect(model.querySelector('span')).not.toBeNull()
    const tool = root.querySelector(`[data-testid="trajectory-bar-map:${rows[2]!.key}"]`) as HTMLElement
    expect(parseFloat(tool.style.left)).toBeGreaterThan(parseFloat(model.style.left))
    expect(root.querySelector('[data-ui-selected]')?.getAttribute('data-testid')).toBe(`trajectory-bar-map:${rows[0]!.key}`)
    tool.click()
    expect(onSelect).toHaveBeenCalledWith(rows[2]!.key)
  })

  it('states when nothing has been mapped yet', () => {
    const root = mount(TrajectoryOverview, { bars: [], selectedKey: null })
    expect(root.textContent).toContain('Nothing to map')
  })
})
