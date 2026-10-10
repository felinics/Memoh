import type { BotsBotCheck } from '@memohai/sdk'

/**
 * The detail text of a runtime check: the catalog copy for the setup error
 * code the workspace recorded, or the text from an earlier server when the
 * record has no code.
 */
export function botCheckDetail(
  check: Pick<BotsBotCheck, 'detail' | 'metadata'>,
  translate: (key: string) => string,
): string {
  const code = check.metadata?.setup_error_code
  if (typeof code === 'string' && code.trim()) return translate(`errors.${code.trim()}`)
  return check.detail?.trim() ?? ''
}
