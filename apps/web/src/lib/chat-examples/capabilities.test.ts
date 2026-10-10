import { describe, expect, it } from 'vitest'
import { capabilitySnapshotFrom } from './capabilities'

describe('capabilitySnapshotFrom', () => {
  it('maps successful probes to known capabilities', () => {
    expect(capabilitySnapshotFrom({
      settings: { search_provider_id: 'p1', memory_enabled: false, display_enabled: false },
      container: { status: 'running' },
    })).toEqual({
      web_search: true,
      memory: false,
      browser: false,
      workspace: true,
      schedule: true,
    })
  })

  it('keeps failed probes unknown instead of missing', () => {
    const snapshot = capabilitySnapshotFrom({})
    expect(snapshot.web_search).toBeUndefined()
    expect(snapshot.memory).toBeUndefined()
    expect(snapshot.browser).toBeUndefined()
    expect(snapshot.workspace).toBeUndefined()
    expect(snapshot.schedule).toBe(true)
  })

  it('reports missing web search when no provider is set', () => {
    expect(capabilitySnapshotFrom({ settings: { search_provider_id: '' } }).web_search).toBe(false)
  })

  it('keeps memory unknown when the server does not report it', () => {
    expect(capabilitySnapshotFrom({ settings: {} }).memory).toBeUndefined()
  })

  it('enables the browser when the desktop display is on', () => {
    expect(capabilitySnapshotFrom({ settings: { display_enabled: true } }).browser).toBe(true)
  })

  it('never reports a missing workspace, since remote and local runtimes have no container', () => {
    expect(capabilitySnapshotFrom({ container: null }).workspace).toBeUndefined()
  })

  it('never reports email or channels, which have no per-bot probe', () => {
    const snapshot = capabilitySnapshotFrom({ settings: {}, container: {} })
    expect('email' in snapshot).toBe(false)
    expect('channel' in snapshot).toBe(false)
  })
})
