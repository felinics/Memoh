export interface GallerySection<T> {
  key: string
  items: T[]
}

// Cuts one page out of sections laid end to end, so the sections share one
// pagination. A section the page starts inside keeps its key, so its title
// shows again at the top of that page; an empty section takes no room.
export function pageSections<T>(sections: readonly GallerySection<T>[], page: number, pageSize: number): GallerySection<T>[] {
  const start = (page - 1) * pageSize
  const end = start + pageSize
  const out: GallerySection<T>[] = []
  let offset = 0
  for (const section of sections) {
    const from = Math.max(start - offset, 0)
    const to = Math.min(end - offset, section.items.length)
    if (from < to) out.push({ key: section.key, items: section.items.slice(from, to) })
    offset += section.items.length
  }
  return out
}
