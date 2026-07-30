import { useStorage } from '@vueuse/core'
import { watch } from 'vue'
import type { Ref } from 'vue'

/**
 * Per-browser-tab state with a cross-session cold-start seed.
 *
 * WHY: "which session / which layout am I looking at" is per-Tab state — two
 * browser tabs are two windows and must not hijack each other. The old design
 * used `useStorage(key, …, localStorage)`, which (a) shares one value across
 * every tab and (b) broadcasts changes via the `storage` event, so tab A's
 * click retargeted tab B's session. sessionStorage is physically per-tab and
 * fires no cross-tab event, so it cannot cause that hijack.
 *
 * The cost of sessionStorage alone: it is wiped when the tab's last copy closes,
 * losing "reopen where I left off". So (opt-in via `seed`) we keep ONE seed in
 * localStorage under the SAME key name — a different storage area, so no
 * collision — read it EXACTLY once when a fresh tab has no sessionStorage value
 * (true cold start), then mirror later writes into it. Same key name also makes
 * migration free: an existing `localStorage[key]` from the old design is picked
 * up as the seed on first load, so no layout/bot selection is lost on deploy.
 *
 * The seed is deliberately last-writer-wins across tabs and is NEVER read again
 * while the tab lives — it is a default, not a live source, so it needs no
 * cross-tab listener and cannot reintroduce the coupling it replaces. The mirror
 * write is a plain `setItem` (NOT a second `useStorage`) for the same reason: a
 * second useStorage would attach a `storage` listener and undo the isolation.
 *
 * ponytail: the seed is a single shared slot, so "reopen after closing all tabs"
 * restores whichever tab wrote last, not a designated primary tab. Tracking a
 * primary tab isn't worth the complexity; a plausible last state is enough.
 */
export function useTabScopedStorage<T>(
  key: string,
  defaults: T,
  opts: { seed?: boolean } = {},
): Ref<T> {
  const ss = typeof sessionStorage !== 'undefined' ? sessionStorage : undefined
  const ls = typeof localStorage !== 'undefined' ? localStorage : undefined

  // Seed at the RAW-string layer, never by re-serializing. useStorage's default
  // ('any') serializer stores a string bare (`abc`) but an object as JSON — if
  // we JSON.parse the seed ourselves, the object case works by luck and the
  // string case throws on the bare value. Copying the raw slot sidesteps that
  // entirely: vueuse owns (de)serialization on both ends, so the seed — incl. a
  // value written by the OLD localStorage design — round-trips byte-identically.
  //
  // Must run BEFORE useStorage: its constructor writes `defaults` into
  // sessionStorage, which would erase the "this tab is cold" signal.
  if (opts.seed && ss && ls && ss.getItem(key) === null) {
    try {
      const seed = ls.getItem(key)
      // Quota / disabled sessionStorage must not abort store construction —
      // fall through to `defaults` the same way the mirror write is best-effort.
      if (seed !== null) ss.setItem(key, seed)
    } catch { /* seed unavailable — use defaults */ }
  }

  const state = useStorage<T>(key, defaults, ss, { listenToStorageChanges: false })

  // Mirror later writes back into the seed slot, again as raw text: read what
  // useStorage just serialized into sessionStorage and copy it verbatim. Plain
  // setItem (NOT a second useStorage) so no `storage` listener is attached —
  // attaching one would reintroduce the cross-tab coupling this file removes.
  if (opts.seed && ss && ls) {
    watch(state, () => {
      const raw = ss.getItem(key)
      try {
        if (raw === null) ls.removeItem(key)
        else ls.setItem(key, raw)
      } catch { /* quota — best effort */ }
    }, { deep: true })
  }

  return state
}
