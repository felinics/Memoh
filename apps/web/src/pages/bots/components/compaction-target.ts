export function isCompactionTargetPercentInvalid(target: number | null): boolean {
  return target !== null && (!Number.isInteger(target) || target < 1 || target > 99)
}

export function compactionTargetPercentAfterToggle(
  enabled: boolean,
  draft: number | null,
  saved: number | null,
): number | null {
  return !enabled && isCompactionTargetPercentInvalid(draft) ? saved : draft
}
