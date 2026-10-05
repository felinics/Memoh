import type { ErrorBlock } from '@/store/chat/types'

export type Translate = (key: string, args?: Record<string, string>) => string
export type HasTranslation = (key: string) => boolean

/**
 * The text of an error block, the same for a live turn and for one loaded from
 * history. A block with a code shows the copy for that code; a code this client
 * has no copy for shows the generic failure copy rather than the server's
 * detail. Only a block without a code shows its content.
 */
export function errorBlockText(
  block: Pick<ErrorBlock, 'code' | 'content'> & { args?: Record<string, string> },
  t: Translate,
  te: HasTranslation,
): string {
  const code = block.code?.trim()
  if (!code) return block.content
  const key = `errors.${code}`
  return te(key) ? t(key, block.args ?? {}) : t('errors.internal')
}
