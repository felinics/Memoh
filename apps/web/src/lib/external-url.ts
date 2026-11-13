export function canOpenDesktopExternalUrl(): boolean {
  return typeof window !== 'undefined' && typeof window.api?.desktop?.openExternalUrl === 'function'
}

export function openDesktopExternalUrl(url: string): Promise<void> | undefined {
  if (typeof window === 'undefined') return undefined
  const open = window.api?.desktop?.openExternalUrl
  return typeof open === 'function' ? Promise.resolve(open(url)) : undefined
}

export function parseSafeHttpUrl(rawUrl: string, errorCode: string): string {
  let parsed: URL
  try {
    parsed = new URL(rawUrl)
  }
  catch {
    throw new Error(errorCode)
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new Error(errorCode)
  }
  return parsed.toString()
}
