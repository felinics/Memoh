import { computed, shallowReactive, unref, type ComputedRef } from 'vue'
import { useRouter, type RouteLocationNormalized, type RouteLocationRaw, type Router } from 'vue-router'
import { useI18n } from 'vue-i18n'

/**
 * Back affordances need to know the *previous* in-app page to (a) decide whether
 * "back" should pop history or fall back to a fixed page, and (b) label itself
 * after where it actually leads.
 *
 * We cannot read this from history state. The web build uses createWebHistory
 * (window.history.state.back), but the desktop shell mounts the very same pages
 * under createMemoryHistory, whose stack lives in a private closure and is never
 * mirrored to window.history nor to router.options.history.state. So any
 * history-state read looks "cold" on desktop and wrongly falls back — which is
 * exactly why the bot back button kept reading "Bots" there.
 *
 * The one signal both backends share is router.afterEach. installBackHistory
 * tracks a small per-router stack from it, giving a backend-agnostic notion of
 * "the page before this one". router.back() itself works on both backends.
 *
 * In-page state (tabs/filters) writes itself into the URL — see useSyncedQueryParam.
 * Those transitions keep the same path, and installBackHistory treats a same-path
 * transition as "not a navigation": it skips updating the predecessor instead of
 * trusting callers to use replace. The contract is enforced here, not assumed, so a
 * tab swap can never make "back" point at (or label itself after) the current page.
 */

interface BackHistory {
  // The entry navigated away from on the latest push — i.e. where back leads.
  // null when there is no in-app predecessor (cold load / first navigation).
  previous: RouteLocationNormalized | null
}

const histories = new WeakMap<Router, BackHistory>()

/**
 * Installs afterEach tracking of the previous route on a router. Call once per
 * router instance, before it is used (see router.ts / desktop chat/router.ts).
 * Idempotent: a second call on the same router is a no-op.
 */
export function installBackHistory(router: Router): void {
  if (histories.has(router)) return
  const state = shallowReactive<BackHistory>({ previous: null })
  histories.set(router, state)
  router.afterEach((to, from) => {
    // Same-path transitions are in-page state syncs (a tab/filter writing itself
    // into the query — see useSyncedQueryParam), not navigations. Skipping them
    // here *enforces* that rule structurally: it holds even if a caller reaches for
    // push instead of replace, so back never points at the page it sits on.
    if (to.path === from.path) return
    // from is START_LOCATION (empty path, no matched records) on the first
    // navigation of the session — treat that as "no predecessor".
    state.previous = from.matched.length > 0 ? from : null
  })
}

/**
 * Resolves a location's display label from the deepest matched route that
 * declares a `breadcrumb` meta. The breadcrumb may be a ref (i18nRef, so the
 * label tracks locale) or a function of the resolved route (e.g. a param), which
 * mirrors how the breadcrumb is declared in router.ts. Returns '' when none of
 * the matched records carry a breadcrumb.
 */
function resolveBreadcrumbLabel(
  router: Router,
  location: RouteLocationRaw,
): string {
  const resolved = router.resolve(location)
  for (let i = resolved.matched.length - 1; i >= 0; i--) {
    const breadcrumb = resolved.matched[i]?.meta?.breadcrumb
    if (breadcrumb == null) continue
    if (typeof breadcrumb === 'function') return String(breadcrumb(resolved))
    return String(unref(breadcrumb))
  }
  return ''
}

/**
 * Read-only reactive access to the tracked previous route (null when the
 * current page was reached cold — fresh window, refresh, deep link).
 *
 * Shells that must distinguish "entered from another in-app page" from
 * "deep-linked" read this — e.g. the mobile settings shell opens on its
 * navigation list only when the user arrived from outside settings. For back
 * buttons themselves, prefer useBackOr / useBackAffordance above.
 */
export function usePreviousRoute(): ComputedRef<RouteLocationNormalized | null> {
  const router = useRouter()
  return computed(() => histories.get(router)?.previous ?? null)
}

/**
 * Builds a click handler for "back" affordances that follows real navigation
 * history instead of hard-coding a destination.
 *
 * - Reached from another in-app page → returns to it via router.back().
 * - Opened cold (fresh window, refresh, deep link) → no predecessor, so it
 *   routes to `fallback` (e.g. the bots list).
 */
export function useBackOr(fallback: RouteLocationRaw) {
  const router = useRouter()
  return () => {
    if (histories.get(router)?.previous != null) {
      router.back()
    } else {
      void router.push(fallback)
    }
  }
}

/**
 * History-following back affordance that also derives its label from where back
 * actually leads, so the text never lies about the destination.
 *
 * - `onBack`: router.back() when there's an in-app predecessor, else push(fallback).
 * - `label`: the previous page's breadcrumb; the fallback's breadcrumb on a cold
 *   load; a generic "Back" if neither resolves one.
 *
 * Requires installBackHistory(router) to have run for the active router.
 */
export function useBackAffordance(fallback: RouteLocationRaw) {
  const router = useRouter()
  const { t } = useI18n()

  const label = computed(() => {
    const previous = histories.get(router)?.previous
    const target: RouteLocationRaw = previous ?? fallback
    return resolveBreadcrumbLabel(router, target) || t('common.back')
  })

  return { onBack: useBackOr(fallback), label }
}
