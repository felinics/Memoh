function truncateProbeError(text: string): string {
  const max = 220
  return text.length > max ? `${text.slice(0, max).trimEnd()}…` : text
}

// The probe detail can embed the raw upstream response inside `[body: …]`. When
// a Base URL points at a website instead of an API the body is a full HTML
// page, so strip the markup down to its visible text (often near-empty) and
// keep only a short, actionable hint instead of dumping the document.
export function formatProbeError(raw: string | undefined, fallback: string): string {
  const text = (raw ?? '').trim()
  if (!text) return fallback
  const bodyStart = text.indexOf('[body:')
  if (bodyStart === -1) return truncateProbeError(text)
  const head = text.slice(0, bodyStart).trim()
  let body = text.slice(bodyStart + '[body:'.length).replace(/\]\s*$/, '').trim()
  if (/<!doctype|<\/?[a-z][^>]*>/i.test(body)) {
    body = body
      .replace(/<(script|style)[^>]*>[\s\S]*?(<\/\1>|$)/gi, ' ')
      .replace(/<[^>]*>/g, ' ')
  }
  body = body.replace(/\s+/g, ' ').trim()
  return truncateProbeError(body ? `${head} · ${body}` : head)
}
