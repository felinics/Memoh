import { describe, expect, it } from 'vitest'
import {
  runtimeCommandComposerText,
  composerLocalQuickActionID,
  visibleRuntimeCommands,
} from './runtime-slash-commands'

describe('runtime slash commands', () => {
  it('projects opaque Agent commands without shadowing Memoh controls', () => {
    const commands = [
      { name: 'review:deep', description: 'Review changes', input_hint: 'scope' },
      { name: 'help', description: 'Reserved by Memoh' },
      { name: '/invalid' },
      { name: 'bad name' },
    ]

    const visible = visibleRuntimeCommands(commands, 'review')
    expect(visible).toEqual([
      { name: 'review:deep', description: 'Review changes', input_hint: 'scope' },
    ])
    expect(runtimeCommandComposerText(visible[0]!)).toBe('/review:deep ')
    expect(visibleRuntimeCommands(commands, '')).toHaveLength(1)
    expect(composerLocalQuickActionID('/compact', true)).toBe('')
  })
})

it('仅声明计划能力时拦截完整 /plan，不吞掉其他运行时命令或普通消息', () => {
  expect(composerLocalQuickActionID(' /PLAN ', true, true)).toBe('plan')
  expect(composerLocalQuickActionID('/plan', true, false)).toBe('')
  expect(composerLocalQuickActionID('/plan my task', true, true)).toBe('')
  expect(composerLocalQuickActionID('/planning', true, true)).toBe('')
})
