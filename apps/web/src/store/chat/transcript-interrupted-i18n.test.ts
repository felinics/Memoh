import { afterEach, describe, expect, it } from 'vitest'
import i18n from '@/i18n'
import type { UIMessage } from '@/composables/api/useChat.types'
import {
  createTranscriptHistory,
  interruptedTurnMarker,
} from './transcript-history'

const originalLocale = i18n.global.locale.value

afterEach(() => {
  i18n.global.locale.value = originalLocale
})

describe('interrupted-turn history localization', () => {
  it('relocalizes an already-normalized marker when the runtime locale changes', () => {
    const history = createTranscriptHistory({
      messages: [],
      rememberBackgroundTask: task => task,
      applyPendingBackgroundEventsToTool: () => {},
    })
    const block = history.normalizeUIMessage({
      id: 1,
      type: 'text',
      content: interruptedTurnMarker,
    } as UIMessage)

    if (block.type !== 'text') throw new Error('expected text block')

    i18n.global.locale.value = 'en'
    const english = block.content
    i18n.global.locale.value = 'zh'
    const chinese = block.content

    expect(english).not.toContain(interruptedTurnMarker)
    expect(chinese).not.toContain(interruptedTurnMarker)
    expect(chinese).not.toBe(english)
  })
})
