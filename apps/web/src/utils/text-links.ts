export interface TextLinkSegment {
  text: string
  href?: string
}

// Bare http(s) addresses inside free-form text. Everything else stays verbatim,
// which is the point: this is for surfaces that must not interpret markdown.
const HTTP_URL_REGEX = /https?:\/\/[^\s<>"'`]+/g

// Sentence punctuation that trails an address belongs to the sentence. A
// closing bracket is kept only when the address opened one.
function trimTrailingPunctuation(href: string): string {
  let end = href.length
  while (end > 0) {
    const char = href.charAt(end - 1)
    if (char === ')' && href.includes('(')) break
    if (!'.,;:!?)]'.includes(char)) break
    end -= 1
  }
  return href.slice(0, end)
}

// Splits text into verbatim runs and linkable addresses, in order.
export function splitTextLinks(text: string): TextLinkSegment[] {
  const segments: TextLinkSegment[] = []
  let cursor = 0
  for (const match of text.matchAll(HTTP_URL_REGEX)) {
    const start = match.index ?? 0
    const href = trimTrailingPunctuation(match[0])
    if (!href) continue
    if (start > cursor) segments.push({ text: text.slice(cursor, start) })
    segments.push({ text: href, href })
    cursor = start + href.length
  }
  if (cursor < text.length) segments.push({ text: text.slice(cursor) })
  return segments
}
