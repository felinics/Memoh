function truncateProbeError(text: string): string {
  const max = 220
  return text.length > max ? `${text.slice(0, max).trimEnd()}…` : text
}

// The probe detail can embed the raw upstream response inside `[body: …]`. When
// a Base URL points at a website instead of an API the body is a full HTML page;
// its visible text is page prose, not an actionable API error. Drop HTML bodies
// entirely and keep only the status head; real JSON API errors remain visible.
export function formatProbeError(raw: string | undefined, fallback: string): string {
  const text = (raw ?? '').trim()
  if (!text) return fallback
  const bodyStart = text.indexOf('[body:')
  if (bodyStart === -1) return truncateProbeError(text)
  const head = text.slice(0, bodyStart).trim()
  const body = text.slice(bodyStart + '[body:'.length).replace(/\]\s*$/, '').trim()
  if (/<!doctype|<\/?[a-z][^>]*>/i.test(body)) return truncateProbeError(head)
  return truncateProbeError(body ? `${head} · ${body}` : head)
}
