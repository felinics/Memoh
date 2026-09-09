export function captureTextPageOffsets(text: string, pageSize = 8192): number[] {
  const size = Number.isFinite(pageSize) ? Math.max(2, Math.trunc(pageSize)) : 8192
  const offsets = [0]
  while (offsets[offsets.length - 1]! < text.length) {
    let end = Math.min(offsets[offsets.length - 1]! + size, text.length)
    const before = text.charCodeAt(end - 1)
    const after = text.charCodeAt(end)
    if ((before >= 0xD800 && before <= 0xDBFF && after >= 0xDC00 && after <= 0xDFFF)
      || (before === 13 && after === 10)) end -= 1
    offsets.push(end)
  }
  if (offsets.length === 1) offsets.push(0)
  return offsets
}
