import {
  createRouter,
  createWebHistory,
} from 'vue-router'
import { useUserStore } from '@/store/user'
import { ensureOnboarding } from '@/router-guards/onboarding'
import { installBackHistory } from '@/composables/useBackOr'
import { createAppRoutes, prefetchSettingsPages } from './routes'


const router = createRouter({
  history: createWebHistory(),
  routes: createAppRoutes('web'),
})

// After the initial navigation settles (entry chunks had bandwidth priority),
// warm the settings pages so first-time sidebar clicks don't wait on a chunk.
void router.isReady().then(() => prefetchSettingsPages())

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
