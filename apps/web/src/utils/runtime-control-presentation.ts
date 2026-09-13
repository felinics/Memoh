import type { ExternalControls, TurnRuntimeCommand, TurnRuntimeMode } from '@memohai/sdk'
import type { CommandActionResult } from '@/composables/api/useChat'

type Translate = (key: string, fallback: string) => string

// Only host-owned declarations carry a catalog key. SDK copy stays authoritative.
function copy(t: Translate, prefix: string | undefined, field: string, native?: string, fallback = ''): string {
  return native || (prefix ? t(`${prefix}.${field}`, fallback) : fallback)
}

export function localizeRuntimeMode(mode: TurnRuntimeMode, t: Translate): TurnRuntimeMode {
  return {
    ...mode,
    name: copy(t, mode.i18n_key, 'name', mode.name, mode.id),
    description: copy(t, mode.i18n_key, 'description', mode.description),
  }
}

export function localizeRuntimeCommand(command: TurnRuntimeCommand, t: Translate): TurnRuntimeCommand {
  return {
    ...command,
    description: copy(t, command.i18n_key, 'description', command.description),
    input_hint: copy(t, command.i18n_key, 'input_hint', command.input_hint),
    running_text: copy(t, command.i18n_key, 'running_text', command.running_text),
    completed_text: copy(t, command.i18n_key, 'completed_text', command.completed_text),
  }
}

export function localizeRuntimeControls(controls: ExternalControls | undefined, t: Translate): ExternalControls | undefined {
  if (!controls) return controls
  return {
    ...controls,
    commands: controls.commands?.map(command => localizeRuntimeCommand(command, t)),
    modes: controls.modes && {
      ...controls.modes,
      available_modes: controls.modes.available_modes?.map(mode => localizeRuntimeMode(mode, t)),
    },
    plan_mode: controls.plan_mode && {
      ...controls.plan_mode,
      available_modes: controls.plan_mode.available_modes?.map(mode => localizeRuntimeMode(mode, t)),
    },
  }
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

// Summarize only fields whose meaning this command owns. Native dictionaries,
// schemas and future fields remain untouched in the JSON detail.
function commandSummary(command: string, data: unknown, t: Translate, locale: string): string | undefined {
  const scalar = (value: unknown): string => typeof value === 'number'
    ? value.toLocaleString(locale)
    : value == null ? t('runtime.result.unknown', '—') : String(value)
  if (command === 'status') {
    const status = record(data)
    if (!status) return undefined
    const lines: string[] = []
    const fields = (source: Record<string, unknown>, keys: string[]) => {
      for (const key of keys) {
        if (key in source && (source[key] === null || typeof source[key] !== 'object')) {
          lines.push(`${t(`runtime.result.fields.${key}`, key)}: ${scalar(source[key])}`)
        }
      }
    }
    fields(status, ['session_id', 'thread_id', 'model', 'permission_mode', 'status', 'tokens', 'context_window', 'limits'])
    const usage = record(status.usage)
    if (usage) fields(usage, ['input_tokens', 'output_tokens', 'cache_read_input_tokens', 'cache_creation_input_tokens'])
    const limits = record(status.limits)
    for (const key of ['primary', 'secondary']) {
      const window = record(limits?.[key])
      if (typeof window?.usedPercent === 'number') {
        lines.push(`${t(`runtime.result.fields.${key}`, key)}: ${scalar(window.usedPercent)}%`)
      }
    }
    return lines.length ? lines.join('\n') : undefined
  }
  if ((command === 'skills' || command === 'mcp') && Array.isArray(data)) {
    if (!data.length) return t('runtime.result.empty', '—')
    const lines = data.flatMap((value) => {
      if (typeof value === 'string') return [`- ${value}`]
      const entry = record(value)
      if (typeof entry?.name !== 'string') return []
      const detail = command === 'skills' ? entry.description : entry.status ?? entry.authStatus
      return [`- ${entry.name}${typeof detail === 'string' && detail ? ` — ${detail}` : ''}`]
    })
    return lines.length ? lines.join('\n') : undefined
  }
  return undefined
}

export function localizeRuntimeCommandResult(result: CommandActionResult, t: Translate, locale: string, command = ''): CommandActionResult {
  const parts: string[] = []
  if (result.notice) parts.push(t(`runtime.result.notices.${result.notice}`, result.notice))
  const text = result.text || (result.text_key ? t(result.text_key, '') : '')
  if (text) parts.push(text)
  const summary = commandSummary(command, result.data, t, locale)
  if (result.data !== undefined) parts.push(summary ?? JSON.stringify(result.data, null, 2))
  return {
    ...result,
    data: summary === undefined ? undefined : result.data,
    text: parts.join('\n\n'),
    items: result.items?.map(item => {
      if (!item.i18n_key) return item
      return {
        ...item,
        title: copy(t, item.i18n_key, 'name', item.title, item.id),
        description: copy(t, item.i18n_key, 'description', item.description),
      }
    }),
  }
}
