// Preset bot names for users who don't want to invent one. Every entry is a
// classic cat name (or the word/sound for "cat") in one of the product's
// locales, so the list works untranslated across en/zh/ja.
export const CAT_NAME_PRESETS = [
  'Neko',
  'Tama',
  'Nyanko',
  'Mimi',
  'Miao',
  'Kitty',
] as const

// Returns a random preset, never repeating `current` when it is one of the
// presets, so re-rolling always produces a visible change.
export function randomCatName(current?: string): string {
  const trimmed = current?.trim()
  const pool = CAT_NAME_PRESETS.filter(name => name !== trimmed)
  return pool[Math.floor(Math.random() * pool.length)] ?? CAT_NAME_PRESETS[0]
}
