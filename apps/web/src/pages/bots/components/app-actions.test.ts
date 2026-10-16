import { describe, expect, it } from 'vitest'
import { appCanTry, appPrimaryAction } from './app-actions'

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

describe('trying an App from the bot', () => {
  const installed = { status: 'installed' as const, installation_id: 'inst-1' }

  it('is offered once the App is installed and idle', () => {
    expect(appCanTry(installed, { readonly: false })).toBe(true)
  })

  it('is not offered while the workspace is not running', () => {
    expect(appCanTry(installed, { readonly: true })).toBe(false)
  })

  it('is offered for Apps discovered in the workspace, which are usable without a Memoh installation', () => {
    expect(appCanTry({ status: 'discovered' }, { readonly: false })).toBe(true)
  })

  it('stays offered when an update is available', () => {
    expect(appCanTry({ ...installed, update_available: true } as never, { readonly: false })).toBe(true)
  })

  it.each(['installing', 'updating', 'removing', 'failed', 'partial'] as const)('is not offered for %s Apps', (status) => {
    expect(appCanTry({ ...installed, status }, { readonly: false })).toBe(false)
  })
})
