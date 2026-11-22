import {
  createRouter,
  createMemoryHistory,
  START_LOCATION,
  type RouteLocationNormalized,
  type RouteRecordRaw,
} from 'vue-router'
import { toast } from '@felinic/ui'
import i18n from '@memohai/web/i18n'
import { createAppRoutes, loadMainSection, showsChatWorkspace } from '@memohai/web/routes'
import { isChunkLoadError, reloadForChunkLoadError } from '@memohai/web/lib/chunk-load-error'
import { ensureOnboarding, hasOnboardingInProgress } from '@memohai/web/router-guards/onboarding'
import { guardAuthenticatedRoute, normalizeRoutePath } from '@memohai/web/router-guards/return-to'
import { useUserStore } from '@memohai/web/store/user'
import { installBackHistory } from '@memohai/web/composables/useBackOr'

const routes: RouteRecordRaw[] = [
  {
    path: '/connect',
    name: 'ConnectServer',
    component: () => import('../connect/ConnectServer.vue'),
  },
  ...createAppRoutes('desktop'),
]

const router = createRouter({
  history: createMemoryHistory(),
  routes,
})

void router.isReady().then(() => {
  if (showsChatWorkspace(router.currentRoute.value)) loadMainSection().catch(() => {})
}, () => {})

// Memory history keeps no readable back-stack, so back affordances rely on this
// afterEach-based tracker instead of history state. See useBackOr.
installBackHistory(router)

router.onError((error: Error, _to, from) => {
  if (isChunkLoadError(error)) {
    if (reloadForChunkLoadError()) {
      console.warn('[Router] Chunk load failed, reloading...', error.message)
      return
    }
    console.error('[Router] Chunk failed to load again after a reload', error)
    if (from !== START_LOCATION) toast.error(i18n.global.t('common.loadFailed'))
    return
  }
  throw error
})

router.beforeEach(async (to: RouteLocationNormalized) => {
  // Dev component wall: allow in dev builds, never reachable in prod.
  const path = normalizeRoutePath(to.path)
  if (path === '/dev' || path.startsWith('/dev/')) {
    return import.meta.env.DEV ? true : { path: '/' }
  }

  if (path === '/connect') return true

  return guardAuthenticatedRoute(to, {
    readToken: () => localStorage.getItem('token'),
    readUserRole: () => useUserStore().userInfo.role,
    hasOnboardingInProgress,
    ensureOnboarding,
  })
})

// Dev convenience: reach the component wall from devtools without a URL bar
// (memory history). e.g. `window.__memohRouter.push('/dev/components')`.
if (import.meta.env.DEV) {
  ;(window as unknown as { __memohRouter?: typeof router }).__memohRouter = router
}

export default router
