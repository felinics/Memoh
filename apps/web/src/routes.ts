import { h } from 'vue'
import { RouterView, type RouteLocationNormalized, type RouteRecordRaw } from 'vue-router'
import { i18nRef } from './i18n'
import { getBotBreadcrumbName } from './lib/bot-breadcrumb'

// ── Settings page loaders ────────────────────────────────────────────────────
// Named consts instead of inline arrows so prefetchSettingsPages warms EXACTLY
// the functions the routes reference — one copy of each import path, no drift.
const pageSettings = () => import('@/pages/settings-section/index.vue')
const pageBots = () => import('@/pages/bots/index.vue')
const pageBotNew = () => import('@/pages/bots/new.vue')
const pageBotCreateProgress = () => import('@/pages/bots/new-progress.vue')
const pageBotDetail = () => import('@/pages/bots/detail.vue')
const pageProviders = () => import('@/pages/providers/index.vue')
const pageRuntimes = () => import('@/pages/runtimes/index.vue')
const pageWebSearch = () => import('@/pages/web-search/index.vue')
const pageMemory = () => import('@/pages/memory/index.vue')
const pageVoice = () => import('@/pages/voice/index.vue')
const pageVideo = () => import('@/pages/video/index.vue')
const pageEmail = () => import('@/pages/email/index.vue')
const pageUsage = () => import('@/pages/usage/index.vue')
const pagePeople = () => import('@/pages/people/index.vue')
const pageAppearance = () => import('@/pages/appearance/index.vue')
const pageKeyboard = () => import('@/pages/keyboard-shortcuts/index.vue')
const pageProfile = () => import('@/pages/profile/index.vue')
const pageSupermarket = () => import('@/pages/supermarket/index.vue')
const pageSupermarketCategory = () => import('@/pages/supermarket/category.vue')
const pageSupermarketAppDetail = () => import('@/pages/supermarket/app-detail.vue')
const pageAbout = () => import('@/pages/about/index.vue')

/**
 * Settings page loaders warmed by prefetchSettingsPages, ordered sidebar
 * destinations FIRST and drill-in sub-pages (only reachable through a list
 * page) LAST — warming drill-ins early just competes with the pages a first
 * click can actually hit. Exported so routes.test.ts can pin it to the route
 * table (adding a settings page without adding its loader fails that test).
 */
export const settingsPageLoaders = [
  pageSettings,
  pageBots, pageProviders, pageRuntimes, pageWebSearch, pageMemory, pageVoice,
  pageVideo, pageEmail, pageUsage, pagePeople, pageAppearance, pageKeyboard,
  pageProfile, pageSupermarket, pageAbout,
  pageBotNew, pageBotCreateProgress, pageBotDetail,
  pageSupermarketCategory, pageSupermarketAppDetail,
]

/**
 * Warm the settings page chunks during browser idle time.
 *
 * Every settings page is a lazy route chunk, and vue-router only starts
 * loading one AFTER the nav click — the URL and the page both wait on the
 * download, so on a slow connection a first-time click reads as a dead click
 * (measured ~0.6-0.7s on Slow 4G). Warming during idle makes first clicks hit
 * the module cache instead (ES module requests dedupe by URL, so the later
 * real navigation costs nothing). Skipped under Save-Data; failures are
 * swallowed — a cold click then just pays the normal lazy-load cost.
 *
 * Chunks are warmed ONE AT A TIME. Firing every loader at once makes a click
 * that lands mid-warmup queue behind the whole swarm — the module map dedupes
 * by URL, so the click cannot be prioritized and can end up slower than the
 * old single lazy import. Sequentially, an in-flight chunk costs a click at
 * most one small chunk, and chunks not yet started leave the connection free
 * for the click itself.
 */
export function prefetchSettingsPages(): void {
  const connection = (navigator as Navigator & { connection?: { saveData?: boolean } }).connection
  if (connection?.saveData) return

  const w = window as Window & {
    requestIdleCallback?: (cb: () => void, opts?: { timeout: number }) => number
  }
  const schedule = (cb: () => void) => {
    if (w.requestIdleCallback) w.requestIdleCallback(cb, { timeout: 3000 })
    else setTimeout(cb, 2000)
  }
  const queue = [...settingsPageLoaders]
  const warmNext = () => {
    const load = queue.shift()
    if (!load) return
    void load().catch(() => {}).finally(() => schedule(warmNext))
  }
  schedule(warmNext)
}

/** Shared page routes; each host owns its router, history, and navigation guards. */
export function createAppRoutes(platform: 'web' | 'desktop'): RouteRecordRaw[] {
  const desktop = platform === 'desktop'
  return [
    {
      path: '/onboarding',
      name: 'onboarding',
      component: () => import('@/pages/onboarding/index.vue'),
    },
    {
      // Chat area. The chat UI (main-section: sidebar + dockview) is mounted
      // persistently in App.vue, NOT here — these routes exist only so the URL
      // (/, /bot/:name) matches and the breadcrumb/active-bot sync works. Their
      // components render nothing; App.vue shows the persistent MainSection on
      // these route names. This is what lets chat survive a trip into settings
      // (fixed overlay) without unmounting/relayout/re-scroll.
      path: '/',
      component: { render: () => null },
      children: [
        {
          name: 'home',
          path: '',
          component: { render: () => null },
          meta: {
            breadcrumb: i18nRef('sidebar.chat'),
          },
        },
        {
          name: 'bot',
          path: desktop ? '/bot/:botName?/:sessionId?' : '/bot/:botName?',
          component: { render: () => null },
          meta: {
            breadcrumb: i18nRef('sidebar.chat'),
          },
        },
        {
          // Backwards-compatible redirect for legacy UUID-based chat links.
          path: desktop ? '/chat/:botName?/:sessionId?' : '/chat/:botName?',
          redirect: (to) => {
            const botName = (to.params.botName as string) ?? ''
            return botName
              ? { name: 'bot', params: { botName, ...(desktop ? { sessionId: to.params.sessionId } : {}) } }
              : { name: 'home' }
          },
        },
      ],
    },
    {
      path: '/settings',
      component: pageSettings,
      // Web needs bare /settings for the mobile navigation list.
      ...(desktop ? { redirect: '/settings/bots' } : {}),
      children: [
        {
          path: 'bots',
          component: { render: () => h(RouterView) },
          meta: {
            breadcrumb: i18nRef('sidebar.bots'),
          },
          children: [
            {
              name: 'bots',
              path: '',
              component: pageBots,
            },
            {
              name: 'bot-new',
              path: 'new',
              component: pageBotNew,
              meta: {
                breadcrumb: i18nRef('bots.createBot'),
              },
            },
            {
              name: 'bot-create-progress',
              path: 'new/progress',
              component: pageBotCreateProgress,
              meta: {
                breadcrumb: i18nRef('bots.createBot'),
              },
            },
            {
              name: 'bot-detail',
              path: ':botName',
              component: pageBotDetail,
              meta: {
                // Resolve the bot's display name from the registry the detail page
                // populates; never echo the raw `bot-<uuid>` route param. Unknown
                // names yield '' so the back affordance shows its generic label.
                breadcrumb: (route: RouteLocationNormalized) =>
                  getBotBreadcrumbName(String(route.params.botName ?? '')),
              },
            },
          ],
        },
        {
          name: 'providers',
          path: 'providers',
          component: pageProviders,
          meta: {
            breadcrumb: i18nRef('sidebar.providers'),
          },
        },
        {
          name: 'runtimes',
          path: 'runtimes',
          component: pageRuntimes,
          meta: {
            breadcrumb: i18nRef('sidebar.runtimes'),
          },
        },
        {
          name: 'web-search',
          path: 'web-search',
          component: pageWebSearch,
          meta: {
            breadcrumb: i18nRef('sidebar.webSearch'),
          },
        },
        {
          name: 'memory',
          path: 'memory',
          component: pageMemory,
          meta: {
            breadcrumb: i18nRef('sidebar.memory'),
          },
        },
        {
          name: 'voice',
          path: 'voice',
          component: pageVoice,
          meta: {
            breadcrumb: i18nRef('sidebar.voice'),
          },
        },
        {
          name: 'video',
          path: 'video',
          component: pageVideo,
          meta: {
            breadcrumb: i18nRef('sidebar.video'),
          },
        },
        // Speech and transcription merged into the Voice page; keep the old paths
        // working for existing links/bookmarks.
        {
          path: 'speech',
          redirect: { name: 'voice' },
        },
        {
          path: 'transcription',
          redirect: { name: 'voice' },
        },
        {
          name: 'email',
          path: 'email',
          component: pageEmail,
          meta: {
            breadcrumb: i18nRef('sidebar.email'),
          },
        },
        {
          name: 'usage',
          path: 'usage',
          component: pageUsage,
          meta: {
            breadcrumb: i18nRef('sidebar.usage'),
          },
        },
        {
          name: 'people',
          path: 'people',
          component: pagePeople,
          meta: {
            breadcrumb: i18nRef('sidebar.people'),
            adminOnly: true,
          },
        },
        {
          name: 'appearance',
          path: 'appearance',
          component: pageAppearance,
          meta: {
            breadcrumb: i18nRef('sidebar.appearance'),
          },
        },
        {
          name: 'keyboard',
          path: 'keyboard',
          component: pageKeyboard,
          meta: {
            breadcrumb: i18nRef('sidebar.keyboard'),
          },
        },
        {
          name: 'profile',
          path: 'profile',
          component: pageProfile,
          meta: {
            breadcrumb: i18nRef('sidebar.settings'),
          },
        },
        {
          path: 'supermarket',
          component: { render: () => h(RouterView) },
          meta: {
            breadcrumb: i18nRef('sidebar.supermarket'),
          },
          children: [
            {
              name: 'supermarket',
              path: '',
              component: pageSupermarket,
            },
            {
              name: 'supermarket-category',
              path: 'category/:categoryId',
              component: pageSupermarketCategory,
              meta: {
                breadcrumb: (route: RouteLocationNormalized) => route.params.categoryId,
              },
            },
            {
              name: 'supermarket-app-detail',
              path: ':registryId/:appId',
              component: pageSupermarketAppDetail,
              meta: {
                breadcrumb: (route: RouteLocationNormalized) => route.params.appId,
              },
            },
          ],
        },
        {
          name: 'about',
          path: 'about',
          component: pageAbout,
          meta: {
            breadcrumb: i18nRef('sidebar.about'),
          },
        },
      ],
    },
    {
      name: 'Login',
      path: '/login',
      component: () => import('@/pages/login/index.vue'),
    },
    {
      name: 'oauth-mcp-callback',
      path: '/oauth/mcp/callback',
      component: () => import('@/pages/oauth/mcp-callback.vue'),
    },
    {
      // Generic provider deep-link target (`#payload=` carries the prefilled
      // provider as base64url JSON; see utils/provider-connect.ts). Standalone
      // confirm page — the key stays in the fragment and is only saved after
      // explicit user confirmation.
      name: 'provider-connect',
      path: '/providers/connect',
      component: () => import('@/pages/providers/connect.vue'),
    },
    // Dev-only component wall. Registered ONLY in dev builds, so the chunk and
    // its auth-bypass guard never exist in production. Reached by setting the
    // `memoh:dev-tools` localStorage flag and navigating to /dev/components.
    ...(import.meta.env.DEV
      ? [
          {
            name: 'dev-components',
            path: '/dev/components',
            component: () => import('@/pages/dev/components/index.vue'),
          },
        ]
      : []),
  ]
}
