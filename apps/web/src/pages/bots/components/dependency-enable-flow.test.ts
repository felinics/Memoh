import { describe, expect, it } from 'vitest'
import {
  agentDependencyRequirement,
  dependencyItemFromPreflight,
  resolveEnableFlowStep,
} from './dependency-enable-flow'

const codex = { dependencyId: 'codex', requiredVersion: '' }

describe('agentDependencyRequirement', () => {
  it('returns null for runtimes without a declaration', () => {
    expect(agentDependencyRequirement({})).toBeNull()
    expect(agentDependencyRequirement({ dependency: { dependency_id: '  ' } })).toBeNull()
    expect(agentDependencyRequirement(null)).toBeNull()
  })

  it('trims the declared id and version hint', () => {
    expect(agentDependencyRequirement({ dependency: { dependency_id: ' codex ', required_version: ' 0.151.0 ' } }))
      .toEqual({ dependencyId: 'codex', requiredVersion: '0.151.0' })
    expect(agentDependencyRequirement({ dependency: { dependency_id: 'codex' } })).toEqual(codex)
  })
})

describe('resolveEnableFlowStep', () => {
  // WD-EXT-004: a stopped or absent workspace is a guidance step, never an
  // automatic start, and it wins over whatever items the answer carries.
  it('routes a non-running workspace to the guidance dialog', () => {
    expect(resolveEnableFlowStep(codex, { workspace_state: 'not_running', items: [] }))
      .toEqual({ kind: 'workspace', state: 'not_running' })
    expect(resolveEnableFlowStep(codex, { workspace_state: 'missing' }))
      .toEqual({ kind: 'workspace', state: 'missing' })
    expect(resolveEnableFlowStep(codex, { workspace_state: 'remote_offline' }))
      .toEqual({ kind: 'remote_offline' })
  })

  it('passes an installed dependency straight through, whatever its version', () => {
    expect(resolveEnableFlowStep(codex, {
      workspace_state: 'running',
      items: [{ dependency_id: 'codex', state: 'satisfied', installed_version: '0.151.0' }],
    })).toEqual({ kind: 'satisfied' })
    // Agent CLIs are not pinned: a version the Server calls mismatched still enables.
    expect(resolveEnableFlowStep(codex, {
      workspace_state: 'running',
      items: [{ dependency_id: 'codex', state: 'version_mismatch', installed_version: '0.147.0', required_version: '0.151.0' }],
    })).toEqual({ kind: 'satisfied' })
  })

  it('asks for an install when the dependency is missing', () => {
    const step = resolveEnableFlowStep(codex, {
      workspace_state: 'running',
      items: [{ dependency_id: 'codex', name: 'Codex', state: 'missing' }],
    })
    expect(step).toMatchObject({ kind: 'install' })
    expect(step.kind === 'install' && step.item).toMatchObject({
      id: 'codex',
      name: 'Codex',
      category: 'agent',
      source: 'managed',
      status: undefined,
      platform_supported: true,
    })
    // No version → the confirm dialog says "latest".
    expect(step.kind === 'install' && step.item.required_version).toBeUndefined()
  })

  it('reports unsupported platforms and unknown answers without an install step', () => {
    expect(resolveEnableFlowStep(codex, {
      workspace_state: 'running',
      items: [{ dependency_id: 'codex', state: 'platform_unsupported' }],
    })).toMatchObject({ kind: 'platform_unsupported', item: { platform_supported: false } })
    expect(resolveEnableFlowStep(codex, {
      workspace_state: 'running',
      items: [{ dependency_id: 'codex', state: 'unknown_dependency' }],
    })).toEqual({ kind: 'unknown' })
    expect(resolveEnableFlowStep(codex, { workspace_state: 'running', items: [{ dependency_id: 'other', state: 'satisfied' }] }))
      .toEqual({ kind: 'unknown' })
    expect(resolveEnableFlowStep(codex, undefined)).toEqual({ kind: 'unknown' })
  })
})

describe('dependencyItemFromPreflight', () => {
  it('falls back to the brand display name and keeps a reported version hint', () => {
    expect(dependencyItemFromPreflight(codex)).toMatchObject({ id: 'codex', name: 'Codex', platform_supported: true })
    expect(dependencyItemFromPreflight(codex, { dependency_id: 'codex', state: 'missing', required_version: '0.151.0' }))
      .toMatchObject({ required_version: '0.151.0' })
  })
})
