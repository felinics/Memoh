import { useMediaQuery } from '@vueuse/core'

/**
 * The ONE JavaScript breakpoint of the app shell: below 768px the chat area
 * swaps from the multi-panel workbench to the single-stack mobile shell.
 *
 * Why a JS breakpoint at all: shell selection (rail vs top bar, multi-group
 * dock vs single stack) is a structural either/or that CSS cannot express
 * without rendering both shells. Everything finer — in-page spacing, font,
 * control density — stays with Tailwind `md:` prefixes; this query is ONLY
 * for picking which shell/navigation form to mount. The two are strict
 * complements: `md:` applies at >= 768px, this at < 768px, so a state change
 * flips exactly one boundary with no overlap zone.
 *
 * Width-only by decision: phone landscape (>= 768px) deliberately falls back
 * to the desktop shell, which is no worse than before this shell existed. A
 * `pointer: coarse` combination (which would also catch 768px portrait
 * tablets) is parked for a later phase.
 *
 * Desktop is physically unreachable by this branch: the Electron window has
 * minWidth 960, so the media query can never match there — no desktop-only
 * guard is needed anywhere this composable is consumed.
 */
export function useIsMobile() {
  // Pass the global window explicitly: vueuse's default `defaultWindow` is
  // captured at ITS module load as `isClient ? window : undefined`, so in a
  // non-browser test runtime that polyfills window later (the store tests do,
  // via vi.hoisted) the default stays undefined and the query would be
  // permanently false. Resolving window at CALL time keeps the composable
  // honest wherever a window exists by the time a store/component runs; in
  // the browser this is the same object vueuse would have defaulted to.
  return useMediaQuery('(width < 768px)', {
    window: typeof window === 'undefined' ? undefined : window,
  })
}
