import { describe, expect, it } from 'vitest'
import { ref } from 'vue'
import { useBotStatusMeta } from './useBotStatusMeta'

const t = (key: string) => key

describe('useBotStatusMeta', () => {
  it('treats creating and deleting as pending', () => {
    const bot = ref({ status: 'creating', is_active: true })
    const meta = useBotStatusMeta(bot, t)
    expect(meta.isPending.value).toBe(true)
    expect(meta.isFailed.value).toBe(false)
    expect(meta.statusLabel.value).toBe('bots.lifecycle.creating')
    expect(meta.statusVariant.value).toBe('secondary')

    bot.value = { status: 'deleting', is_active: true }
    expect(meta.isPending.value).toBe(true)
    expect(meta.statusLabel.value).toBe('bots.lifecycle.deleting')
  })

  it('renders a failed creation as a destructive, non-pending state', () => {
    const bot = ref({ status: 'failed', is_active: true, check_state: 'ok' })
    const meta = useBotStatusMeta(bot, t)
    expect(meta.isFailed.value).toBe(true)
    // Failed bots stay actionable (retry or delete), so they must not be
    // disabled the way pending lifecycle states are.
    expect(meta.isPending.value).toBe(false)
    expect(meta.statusVariant.value).toBe('destructive')
    expect(meta.statusLabel.value).toBe('bots.lifecycle.createFailed')
  })

  it('prefers the failed label over issue counts', () => {
    const bot = ref({ status: 'failed', is_active: true, check_state: 'issue', check_issue_count: 2 })
    const meta = useBotStatusMeta(bot, t)
    expect(meta.hasIssue.value).toBe(true)
    expect(meta.statusLabel.value).toBe('bots.lifecycle.createFailed')
  })

  it('falls back to active/inactive for ready bots', () => {
    const bot = ref({ status: 'ready', is_active: false })
    const meta = useBotStatusMeta(bot, t)
    expect(meta.isFailed.value).toBe(false)
    expect(meta.statusLabel.value).toBe('bots.inactive')
    expect(meta.statusVariant.value).toBe('secondary')
  })
})
