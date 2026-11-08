import {
  createRouter,
  createWebHistory,
} from 'vue-router'
import { useUserStore } from '@/store/user'
import { ensureOnboarding, hasOnboardingInProgress } from '@/router-guards/onboarding'
import { installBackHistory } from '@/composables/useBackOr'
import { isChunkLoadError } from '@/lib/chunk-load-error'
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

// Handle chunk load errors (e.g. user aborted refresh, network failure, new deployment)
router.onError((error) => {
  if (isChunkLoadError(error)) {
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
