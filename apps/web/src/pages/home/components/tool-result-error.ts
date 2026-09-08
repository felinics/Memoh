import type { ToolCallBlock } from '@/store/chat-list'

function asObject(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown> : {}
}

// This is the tool-result protocol flag, not a turn-level failure. Plain text
// can also be a successful result; never infer failure from its wording.
export function hasToolResultError(block: Pick<ToolCallBlock, 'done' | 'result'>): boolean {
  if (!block.done) return false
  const result = asObject(block.result)
  return result.isError === true || asObject(result.structuredContent).isError === true
}

export function toolResultErrorText(result: unknown): string {
  const outer = asObject(result)
  const structured = asObject(outer.structuredContent)
  const texts: string[] = []
  for (const value of [outer.content, structured.content]) {
    if (typeof value === 'string' && value.trim()) texts.push(value)
    if (Array.isArray(value)) {
      for (const part of value) {
        const item = asObject(part)
        if (item.type === 'text' && typeof item.text === 'string' && item.text.trim()) texts.push(item.text)
      }
    }
  }
  // Some tools return only structured diagnostics (error/message, exit status,
  // stdout/stderr, etc.). Preserve those fields instead of showing an empty panel.
  for (const value of [structured, outer]) {
    const fields = Object.fromEntries(Object.entries(value)
      .filter(([key]) => !['isError', 'content', 'structuredContent'].includes(key)))
    if (Object.keys(fields).length) texts.push(JSON.stringify(fields))
  }
  return [...new Set(texts)].join('\n')
}
