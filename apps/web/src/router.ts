import {
  createRouter,
  createWebHistory,
  START_LOCATION,
} from 'vue-router'
import { toast } from '@felinic/ui'
import { useUserStore } from '@/store/user'
import { ensureOnboarding, hasOnboardingInProgress } from '@/router-guards/onboarding'
import { installBackHistory } from '@/composables/useBackOr'
import { isChunkLoadError, reloadForChunkLoadError } from '@/lib/chunk-load-error'
import { showBootFailure } from '@/lib/boot-splash'
import i18n from './i18n'
import { createAppRoutes, loadMainSection, showsChatWorkspace } from './routes'


const router = createRouter({
  history: createWebHistory(),
  routes: createAppRoutes('web'),
})

// App.vue loads the chat workspace when it mounts, and it mounts only after
// the starting language has loaded. On routes that show the workspace, start
// the download as soon as the first route is known instead, alongside the
// language (measured on Slow 4G: the chat appeared about 0.6s sooner); App.vue
// then gets the same request. A failure here is left to App.vue, which reloads
// the page. isReady rejects when the initial navigation errors (e.g. backend
// down); there is nothing to start then.
void router.isReady().then(() => {
  if (showsChatWorkspace(router.currentRoute.value)) loadMainSection().catch(() => {})
}, () => {})

// Track the previous route so history-following back affordances work the same
// on web and on the desktop shell's memory-history router. See useBackOr.
installBackHistory(router)

// Handle chunk load errors (e.g. user aborted refresh, network failure, new
// deployment) by reloading the page, at most once a minute (see
// reloadForChunkLoadError). Failing again within that, the navigation is
// dropped: the first navigation has no page to fall back on, so the loading
// splash switches to its failure state; a later one keeps the current page and
// says so, and clicking the link again retries the download.
router.onError((error, _to, from) => {
  if (isChunkLoadError(error)) {
    if (reloadForChunkLoadError()) {
      console.warn('[Router] Chunk load failed, reloading...', error.message)
      return
    }
    console.error('[Router] Chunk failed to load again after a reload', error)
    if (from === START_LOCATION) showBootFailure(error.message)
    else toast.error(i18n.global.t('common.loadFailed'))
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
    if (hasOnboardingInProgress()) return true
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
