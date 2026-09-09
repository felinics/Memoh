interface ProcessBlockIdentity {
  type: string
  id: number
}

// The backend preserves block ids across the live -> terminal projection, and
// the transcript store preserves the assistant turn's render id across that
// same handover. Combining the two gives UI-only process state a stable scope
// without treating mutable reasoning text as identity.
export function processBlockKey(messageId: string, block: ProcessBlockIdentity): string {
  const scope = messageId.trim()
  if (!scope) return ''
  return `${scope.length}:${scope}/${block.type}:${block.id}`
}
