import {
  createRouter,
  createWebHistory,
  type RouteLocationNormalized,
} from 'vue-router'
import { h } from 'vue'
import { RouterView } from 'vue-router'
import { i18nRef } from './i18n'
import { useUserStore } from '@/store/user'
import { ensureOnboarding } from '@/router-guards/onboarding'
import { installBackHistory } from '@/composables/useBackOr'
import { getBotBreadcrumbName } from '@/lib/bot-breadcrumb'

const routes = [
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
        path: '/bot/:botName?',
        component: { render: () => null },
        meta: {
          breadcrumb: i18nRef('sidebar.chat'),
        },
      },
      {
        // Backwards-compatible redirect for legacy UUID-based chat links.
        path: '/chat/:botName?',
        redirect: (to) => {
          const botName = (to.params.botName as string) ?? ''
          return botName
            ? { name: 'bot', params: { botName } }
            : { name: 'home' }
        },
      },
    ],
  },
  {
    path: '/settings',
    component: () => import('@/pages/settings-section/index.vue'),
    // No redirect: below the JS breakpoint (<768px) bare /settings IS the
    // mobile settings nav list — a real addressable level of the stack, so
    // system back and the shell's ← walk the same history. The desktop
    // default (/settings/bots) is re-applied by a guard in settings-section.
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
            component: () => import('@/pages/bots/index.vue'),
          },
          {
            name: 'bot-new',
            path: 'new',
            component: () => import('@/pages/bots/new.vue'),
            meta: {
              breadcrumb: i18nRef('bots.createBot'),
            },
          },
          {
            name: 'bot-create-progress',
            path: 'new/progress',
            component: () => import('@/pages/bots/new-progress.vue'),
            meta: {
              breadcrumb: i18nRef('bots.createBot'),
            },
          },
          {
            name: 'bot-detail',
            path: ':botName',
            component: () => import('@/pages/bots/detail.vue'),
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
        component: () => import('@/pages/providers/index.vue'),
        meta: {
          breadcrumb: i18nRef('sidebar.providers'),
        },
      },
      {
        name: 'runtimes',
        path: 'runtimes',
        component: () => import('@/pages/runtimes/index.vue'),
        meta: {
          breadcrumb: i18nRef('sidebar.runtimes'),
        },
      },
      {
        name: 'web-search',
        path: 'web-search',
        component: () => import('@/pages/web-search/index.vue'),
        meta: {
          breadcrumb: i18nRef('sidebar.webSearch'),
        },
      },
      {
        name: 'memory',
        path: 'memory',
        component: () => import('@/pages/memory/index.vue'),
        meta: {
          breadcrumb: i18nRef('sidebar.memory'),
        },
      },
      {
        name: 'voice',
        path: 'voice',
        component: () => import('@/pages/voice/index.vue'),
        meta: {
          breadcrumb: i18nRef('sidebar.voice'),
        },
      },
      {
        name: 'video',
        path: 'video',
        component: () => import('@/pages/video/index.vue'),
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
        component: () => import('@/pages/email/index.vue'),
        meta: {
          breadcrumb: i18nRef('sidebar.email'),
        },
      },
      {
        name: 'usage',
        path: 'usage',
        component: () => import('@/pages/usage/index.vue'),
        meta: {
          breadcrumb: i18nRef('sidebar.usage'),
        },
      },
      {
        name: 'people',
        path: 'people',
        component: () => import('@/pages/people/index.vue'),
        meta: {
          breadcrumb: i18nRef('sidebar.people'),
          adminOnly: true,
        },
      },
      {
        name: 'appearance',
        path: 'appearance',
        component: () => import('@/pages/appearance/index.vue'),
        meta: {
          breadcrumb: i18nRef('sidebar.appearance'),
        },
      },
      {
        name: 'keyboard',
        path: 'keyboard',
        component: () => import('@/pages/keyboard-shortcuts/index.vue'),
        meta: {
          breadcrumb: i18nRef('sidebar.keyboard'),
        },
      },
      {
        name: 'profile',
        path: 'profile',
        component: () => import('@/pages/profile/index.vue'),
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
            component: () => import('@/pages/supermarket/index.vue'),
          },
          {
            name: 'supermarket-package-detail',
            path: 'skills/:registryId/:packageId',
            component: () => import('@/pages/supermarket/package-detail.vue'),
            meta: {
              breadcrumb: (route: RouteLocationNormalized) => route.params.packageId,
            },
          },
        ],
      },
      {
        name: 'about',
        path: 'about',
        component: () => import('@/pages/about/index.vue'),
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

const router = createRouter({
  history: createWebHistory(),
  routes,
})

// Track the previous route so history-following back affordances work the same
// on web and on the desktop shell's memory-history router. See useBackOr.
installBackHistory(router)

// Handle chunk load errors (e.g. user aborted refresh, network failure, new deployment)
router.onError((error) => {
  const isChunkLoadError =
    error.message.includes('Failed to fetch dynamically imported module') ||
    error.message.includes('Importing a module script failed') ||
    error.message.includes('error loading dynamically imported module')
  if (isChunkLoadError) {
    console.warn('[Router] Chunk load failed, reloading...', error.message)
    window.location.reload()
    return
  }
  throw error
})

router.beforeEach(async (to) => {
  // Dev component wall: only reachable in dev builds with the flag set.
  if (to.path.startsWith('/dev/')) {
    return import.meta.env.DEV
      && localStorage.getItem('memoh:dev-tools') === '1'
      ? true
      : { path: '/' }
  }

  const token = localStorage.getItem('token')

  if (to.fullPath === '/login') {
    return token ? { path: '/' } : true
  }
  if (to.path.startsWith('/oauth/')) {
    return true
  }
  if (!token) {
    return { name: 'Login' }
  }
  if (to.meta.adminOnly) {
    const userStore = useUserStore()
    if (String(userStore.userInfo.role).toLowerCase() !== 'admin') {
      return { name: 'bots' }
    }
  }

  if (to.path === '/onboarding') {
    const completed = await ensureOnboarding()
    return completed ? { path: '/' } : true
  }

  const completed = await ensureOnboarding()
  if (!completed) {
    return { path: '/onboarding' }
  }

  return true
})

export default router
