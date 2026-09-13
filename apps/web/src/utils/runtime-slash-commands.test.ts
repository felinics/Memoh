import { describe, expect, it } from 'vitest'
import {
  runtimeCommandComposerText,
  composerLocalQuickActionID,
  visibleRuntimeCommands,
  runtimeGoalObjective,
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

it('仅在入口可用时拦截完整 /plan 和 /goal，带参数的命令仍交给运行时', () => {
  expect(composerLocalQuickActionID(' /PLAN ', true, true)).toBe('plan')
  expect(composerLocalQuickActionID('/plan', true, false)).toBe('')
  expect(composerLocalQuickActionID('/plan my task', true, true)).toBe('')
  expect(composerLocalQuickActionID('/planning', true, true)).toBe('')
  expect(composerLocalQuickActionID(' /GOAL ', true, false, true)).toBe('goal')
  expect(composerLocalQuickActionID('/goal', true, false, false)).toBe('')
  expect(composerLocalQuickActionID('/goal my task', true, false, true)).toBe('')
  expect(composerLocalQuickActionID('/goal clear', true, false, true)).toBe('')
  expect(runtimeGoalObjective('/goal 介绍 Memoh', 'claude-code')).toBe('介绍 Memoh')
  expect(runtimeGoalObjective('/goal', 'claude-code')).toBeNull()
  expect(runtimeGoalObjective('/goal clear', 'claude-code')).toBeNull()
  expect(runtimeGoalObjective('/goal resume', 'claude-code')).toBe('resume')
  expect(runtimeGoalObjective('/goal resume', 'codex')).toBeNull()
  expect(runtimeGoalObjective('/goal 介绍 Memoh', 'model')).toBeNull()
})
