import { describe, expect, it } from 'vitest'
import {
  acpSlashCommandComposerText,
  composerLocalQuickActionID,
  visibleACPSlashCommands,
} from './acp-slash-commands'

describe('ACP slash commands', () => {
  it('projects opaque Agent commands without shadowing Memoh controls', () => {
    const commands = [
      { name: 'review:deep', description: 'Review changes', input_hint: 'scope' },
      { name: 'help', description: 'Reserved by Memoh' },
      { name: '/invalid' },
      { name: 'bad name' },
    ]

    const visible = visibleACPSlashCommands(commands, 'review')
    expect(visible).toEqual([
      { name: 'review:deep', description: 'Review changes', input_hint: 'scope' },
    ])
    expect(acpSlashCommandComposerText(visible[0]!)).toBe('/review:deep ')
    expect(visibleACPSlashCommands(commands, '')).toHaveLength(1)
    expect(composerLocalQuickActionID('/compact', true)).toBe('')
  })
})
