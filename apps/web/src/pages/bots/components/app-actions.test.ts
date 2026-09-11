import { describe, expect, it } from 'vitest'
import { appPrimaryAction } from './app-actions'

describe('failed App recovery action', () => {
  it('labels materialization as restoring installation, never as a generic retry', () => {
    expect(appPrimaryAction({ status: 'failed' }, { busy: false, ownsStream: false, readonly: false }))
      .toMatchObject({ action: 'resume', labelKey: 'apps.action.restore', disabled: false })
  })

  it('keeps the existing busy and read-only guards', () => {
    for (const options of [{ busy: true, readonly: false }, { busy: false, readonly: true }]) {
      expect(appPrimaryAction({ status: 'failed' }, { ...options, ownsStream: false })?.disabled).toBe(true)
    }
  })
})
