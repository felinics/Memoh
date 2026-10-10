import { describe, expect, it } from 'vitest'
import { useAsyncConfirm } from './useAsyncConfirm'

describe('useAsyncConfirm', () => {
  it('opens on ask and resolves with the answer', async () => {
    const confirm = useAsyncConfirm()
    const answer = confirm.ask()
    expect(confirm.open.value).toBe(true)
    confirm.answer(true)
    expect(confirm.open.value).toBe(false)
    await expect(answer).resolves.toBe(true)
  })

  it('treats closing the dialog as a no', async () => {
    const confirm = useAsyncConfirm()
    const answer = confirm.ask()
    confirm.setOpen(false)
    await expect(answer).resolves.toBe(false)
  })

  it('declines an unanswered question when a new one is asked', async () => {
    const confirm = useAsyncConfirm()
    const first = confirm.ask()
    const second = confirm.ask()
    await expect(first).resolves.toBe(false)
    confirm.answer(true)
    await expect(second).resolves.toBe(true)
  })
})
