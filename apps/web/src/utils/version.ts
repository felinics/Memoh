const SEMVER_PATTERN = /^\d+\.\d+/

/** Strip a leading "v" prefix so callers can normalize raw version strings uniformly. */
export function normalizeVersion(version?: string | null): string {
  return (version ?? '').trim().replace(/^v/i, '')
}

/** Match numeric semantic versions (e.g. 1.2.3 or v1.2.3-rc.1); returns false for non-release tags such as "dev". */
export function isSemverVersion(version?: string | null): boolean {
  return SEMVER_PATTERN.test(normalizeVersion(version))
}

/** Format a release version as "v<semver>", while leaving non-semver labels such as "dev" verbatim. */
export function formatVersionLabel(version?: string | null): string {
  const normalized = normalizeVersion(version)
  if (!normalized) return ''
  return isSemverVersion(normalized) ? `v${normalized}` : normalized
}
