export type ApprovalTool = 'read' | 'write' | 'exec'
export type ToolApprovalMode = 'allow' | 'ask' | 'deny'
export type WorkspaceTargetKind = 'native' | 'remote'

export interface ToolApprovalFilePolicy {
  mode: ToolApprovalMode
  require_approval: boolean
  bypass_globs: string[]
  force_review_globs: string[]
}

export interface ToolApprovalExecPolicy {
  mode: ToolApprovalMode
  require_approval: boolean
  bypass_commands: string[]
  force_review_commands: string[]
}

export interface ToolApprovalConfig {
  enabled: boolean
  read: ToolApprovalFilePolicy
  write: ToolApprovalFilePolicy
  exec: ToolApprovalExecPolicy
}

export type ToolApprovalModes = Partial<Record<ApprovalTool, unknown>>

interface RawFilePolicy {
  mode?: unknown
  require_approval?: unknown
  bypass_globs?: unknown
  force_review_globs?: unknown
}

interface RawExecPolicy {
  mode?: unknown
  require_approval?: unknown
  bypass_commands?: unknown
  force_review_commands?: unknown
}

interface RawToolApprovalConfig {
  enabled?: unknown
  read?: RawFilePolicy
  write?: RawFilePolicy
  edit?: RawFilePolicy
  exec?: RawExecPolicy
}

export function defaultToolApprovalConfig(kind: WorkspaceTargetKind = 'native'): ToolApprovalConfig {
  return {
    enabled: kind === 'remote',
    read: {
      mode: 'allow',
      require_approval: false,
      bypass_globs: [],
      force_review_globs: [],
    },
    write: {
      mode: 'ask',
      require_approval: true,
      bypass_globs: kind === 'native' ? ['/data/**', '/tmp/**'] : [],
      force_review_globs: [],
    },
    exec: {
      mode: kind === 'remote' ? 'ask' : 'allow',
      require_approval: kind === 'remote',
      bypass_commands: [],
      force_review_commands: [],
    },
  }
}

export function cloneToolApprovalConfig(config: ToolApprovalConfig): ToolApprovalConfig {
  return {
    enabled: config.enabled,
    read: {
      ...config.read,
      bypass_globs: [...config.read.bypass_globs],
      force_review_globs: [...config.read.force_review_globs],
    },
    write: {
      ...config.write,
      bypass_globs: [...config.write.bypass_globs],
      force_review_globs: [...config.write.force_review_globs],
    },
    exec: {
      ...config.exec,
      bypass_commands: [...config.exec.bypass_commands],
      force_review_commands: [...config.exec.force_review_commands],
    },
  }
}

function normalizeStringList(raw: unknown, fallback: string[]): string[] {
  if (!Array.isArray(raw)) return [...fallback]
  return raw.filter((item): item is string => typeof item === 'string')
}

function normalizeMode(
  raw: unknown,
  effective: unknown,
  requireApproval: unknown,
  fallback: ToolApprovalMode,
): ToolApprovalMode {
  for (const value of [raw, effective]) {
    if (value === 'allow' || value === 'ask' || value === 'deny') return value
  }
  if (typeof requireApproval === 'boolean') return requireApproval ? 'ask' : 'allow'
  return fallback
}

function mergeStringLists(...lists: string[][]): string[] {
  const seen = new Set<string>()
  const merged: string[] = []
  for (const list of lists) {
    for (const item of list) {
      if (seen.has(item)) continue
      seen.add(item)
      merged.push(item)
    }
  }
  return merged
}

function normalizeFilePolicy(
  raw: unknown,
  defaults: ToolApprovalFilePolicy,
  effectiveMode?: unknown,
): ToolApprovalFilePolicy {
  const value = raw && typeof raw === 'object' ? raw as RawFilePolicy : {}
  const mode = normalizeMode(value.mode, effectiveMode, value.require_approval, defaults.mode)
  return {
    mode,
    require_approval: mode === 'ask',
    bypass_globs: normalizeStringList(value.bypass_globs, defaults.bypass_globs),
    force_review_globs: normalizeStringList(value.force_review_globs, defaults.force_review_globs),
  }
}

function normalizeExecPolicy(
  raw: unknown,
  defaults: ToolApprovalExecPolicy,
  effectiveMode?: unknown,
): ToolApprovalExecPolicy {
  const value = raw && typeof raw === 'object' ? raw as RawExecPolicy : {}
  const mode = normalizeMode(value.mode, effectiveMode, value.require_approval, defaults.mode)
  return {
    mode,
    require_approval: mode === 'ask',
    bypass_commands: normalizeStringList(value.bypass_commands, defaults.bypass_commands),
    force_review_commands: normalizeStringList(value.force_review_commands, defaults.force_review_commands),
  }
}

function stricterMode(left: ToolApprovalMode, right: ToolApprovalMode): ToolApprovalMode {
  if (left === 'deny' || right === 'deny') return 'deny'
  if (left === 'ask' || right === 'ask') return 'ask'
  return 'allow'
}

function mergeFilePolicies(base: ToolApprovalFilePolicy, legacy: ToolApprovalFilePolicy): ToolApprovalFilePolicy {
  const mode = stricterMode(base.mode, legacy.mode)
  return {
    mode,
    require_approval: mode === 'ask',
    bypass_globs: mergeStringLists(base.bypass_globs, legacy.bypass_globs),
    force_review_globs: mergeStringLists(base.force_review_globs, legacy.force_review_globs),
  }
}

export function normalizeToolApprovalConfig(
  raw: unknown,
  effectiveModes: ToolApprovalModes = {},
  kind: WorkspaceTargetKind = 'native',
): ToolApprovalConfig {
  const defaults = defaultToolApprovalConfig(kind)
  if (!raw || typeof raw !== 'object') {
    return {
      ...defaults,
      read: normalizeFilePolicy(undefined, defaults.read, effectiveModes.read),
      write: normalizeFilePolicy(undefined, defaults.write, effectiveModes.write),
      exec: normalizeExecPolicy(undefined, defaults.exec, effectiveModes.exec),
    }
  }

  const value = raw as RawToolApprovalConfig
  const write = normalizeFilePolicy(value.write, defaults.write, effectiveModes.write)
  return {
    enabled: typeof value.enabled === 'boolean' ? value.enabled : defaults.enabled,
    read: normalizeFilePolicy(value.read, defaults.read, effectiveModes.read),
    write: value.edit
      ? mergeFilePolicies(write, normalizeFilePolicy(value.edit, defaults.write, effectiveModes.write))
      : write,
    exec: normalizeExecPolicy(value.exec, defaults.exec, effectiveModes.exec),
  }
}

export function parseToolApprovalRules(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map(item => item.trim())
    .filter(Boolean)
}

export function formatToolApprovalRules(rules: string[]): string {
  return rules.join('\n')
}

export function toolApprovalConfigsEqual(left: ToolApprovalConfig, right: ToolApprovalConfig): boolean {
  return JSON.stringify(left) === JSON.stringify(right)
}
