// @vitest-environment jsdom
import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import type { ToolCallBlock } from '@/store/chat-list'

// The detail components pulled in by the registry reach a store that builds the
// app-wide i18n instance on import, which needs a browser. This suite only
// exercises pure display resolution, so that instance is stubbed away.
vi.mock('@/i18n', () => ({
  default: { global: { t: (key: string) => key } },
  i18nRef: (key: string) => ({ value: key }),
}))
import en from '@/i18n/locales/en.json'
import ja from '@/i18n/locales/ja.json'
import zh from '@/i18n/locales/zh.json'
import { getToolDisplay } from './tool-call-registry'

// The backend tool catalog is the source of truth for what a session can call.
// Reading it here is what keeps a newly added tool from silently landing in the
// generic "Tool <name>" row: the test fails the moment the two drift.
function repoFile(relativePath: string): string {
  let dir = process.cwd()
  for (let depth = 0; depth < 6; depth++) {
    const candidate = path.join(dir, relativePath)
    if (existsSync(candidate)) return candidate
    dir = path.dirname(dir)
  }
  throw new Error(`not found from ${process.cwd()}: ${relativePath}`)
}

const catalogSource = readFileSync(
  repoFile('internal/agent/tool/internal/toolname/toolname.go'),
  'utf8',
)

const BUILT_IN_TOOLS = [
  ...new Set([
    ...[...catalogSource.matchAll(/newName\("([a-z_0-9]+)"\)/g)].map(match => match[1]!),
    // Two catalog entries name their tool through a constant in another
    // package, so the literal scan above cannot see them.
    'search_memory',
    'ask_user',
  ]),
]

const registrySource = readFileSync(
  repoFile('apps/web/src/pages/home/components/tool-call-registry.ts'),
  'utf8',
)

const GUI_ICON_MAPS: Record<string, string> = {
  BROWSER_ACTION_ICONS: 'browserAction',
  BROWSER_OBSERVE_ICONS: 'browserObserve',
  COMPUTER_OBSERVE_ICONS: 'computerObserve',
  COMPUTER_ACTION_ICONS: 'computerAction',
  REMOTE_SESSION_ICONS: 'remoteSession',
}

function guiActions(constName: string): string[] {
  const block = registrySource.match(new RegExp(`${constName}: Record<string, Component> = \\{([\\s\\S]*?)\\n\\}`))
  expect(block, `${constName} not found`).toBeTruthy()
  return [...block![1]!.matchAll(/^\s*([a-z_0-9]+):/gm)].map(match => match[1]!)
}

// Keys the registry builds by interpolation, which a literal scan cannot see.
const INTERPOLATED_ACTION_KEYS = [
  'list_sessions_chat',
  'list_sessions_schedule',
  'search_messages_user',
  'search_messages_assistant',
]

function actionKeys(): string[] {
  const literals = [...registrySource.matchAll(/actionKey: '([a-z_0-9]+)'/g)].map(match => match[1]!)
  const gui = Object.entries(GUI_ICON_MAPS).flatMap(
    ([constName, namespace]) => guiActions(constName).map(action => `${namespace}.${action}`),
  )
  return [...new Set([...literals, ...gui, ...INTERPOLATED_ACTION_KEYS])]
}

function lookup(messages: unknown, key: string): unknown {
  return key.split('.').reduce<unknown>(
    (node, part) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined),
    messages,
  )
}

function toolBlock(toolName: string, input: Record<string, unknown> = {}): ToolCallBlock {
  return { toolCallId: 'call_1', toolName, input, result: null, done: true } as unknown as ToolCallBlock
}

describe('tool call registry', () => {
  it('reads the backend tool catalog', () => {
    expect(BUILT_IN_TOOLS.length).toBeGreaterThan(40)
    expect(BUILT_IN_TOOLS).toContain('browser_action')
  })

  it('gives every built-in tool its own label instead of the generic fallback', () => {
    const unnamed = BUILT_IN_TOOLS.filter(name => getToolDisplay(toolBlock(name)).actionKey === 'generic')
    expect(unnamed).toEqual([])
  })

  it('lets every built-in tool expose its input and result', () => {
    const opaque = BUILT_IN_TOOLS.filter((name) => {
      const display = getToolDisplay(toolBlock(name))
      return !display.detail && display.expandable !== true
    })
    expect(opaque).toEqual([])
  })

  it.each([['en', en], ['zh', zh], ['ja', ja]])('translates every action key in %s', (_locale, messages) => {
    const missing = actionKeys().filter(key => typeof lookup(messages.chat.tools, key) !== 'string')
    expect(missing).toEqual([])
  })

  it('labels the GUI actions it draws an icon for', () => {
    for (const [constName, namespace] of Object.entries(GUI_ICON_MAPS)) {
      const labels = lookup(en.chat.tools, namespace) as Record<string, string>
      expect(Object.keys(labels).sort()).toEqual(guiActions(constName).sort())
    }
  })

  it('names the parameter variants that change what a call does', () => {
    expect(getToolDisplay(toolBlock('exec', { command: 'ls', run_in_background: true })).actionKey)
      .toBe('exec_background')
    expect(getToolDisplay(toolBlock('read', { path: '/tmp/shot.png' })).actionKey).toBe('read_image')
    expect(getToolDisplay(toolBlock('read', { path: 'a.ts', line_offset: 10, n_lines: 5 })).actionKey)
      .toBe('read_range')
    expect(getToolDisplay(toolBlock('spawn_agent', { task: 't', fork: true })).actionKey)
      .toBe('spawn_agent_fork')
    expect(getToolDisplay(toolBlock('send', { target: 'u', reply_to: 'm1' })).actionKey).toBe('send_reply')
    expect(getToolDisplay(toolBlock('update_schedule', { id: 's1', enabled: false })).actionKey)
      .toBe('update_schedule_disable')
    expect(getToolDisplay(toolBlock('computer_action', { action: 'click', button: 'right' })).actionKey)
      .toBe('computerAction.click_right')
    expect(getToolDisplay(toolBlock('browser_action', { action: 'scroll', direction: 'up' })).actionKey)
      .toBe('browserAction.scroll_up')
    // An unknown modifier keeps the plain action rather than inventing a key.
    expect(getToolDisplay(toolBlock('browser_action', { action: 'scroll', direction: 'sideways' })).actionKey)
      .toBe('browserAction.scroll')
  })

  it('marks failed calls, including a non-zero exec exit', () => {
    expect(getToolDisplay(toolBlock('exec', { command: 'false' })).isError).toBeUndefined()

    const failedExec = { ...toolBlock('exec', { command: 'false' }), result: { exit_code: 1 } } as ToolCallBlock
    expect(getToolDisplay(failedExec).isError).toBe(true)
    expect(getToolDisplay(failedExec).exitCode).toBe(1)

    const failedTool = { ...toolBlock('web_search', { query: 'x' }), result: { isError: true } } as ToolCallBlock
    expect(getToolDisplay(failedTool).isError).toBe(true)
  })
})
