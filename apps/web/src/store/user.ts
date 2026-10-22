import { defineStore } from 'pinia'
import { nextTick, reactive, ref, watch } from 'vue'
import { StorageSerializers, useLocalStorage } from '@vueuse/core'
import { useRouter } from 'vue-router'
import { useQueryCache } from '@pinia/colada'
import { getUsersMe, putUsersMe } from '@memohai/sdk'
import { useChatSelectionStore } from '@/store/chat-selection'
import { notifyAuthSessionCleared, onAuthSessionCleared, type AuthSessionClearReason } from '@/lib/auth-session'
import { cancelPendingQueryCacheSave, removeQueryCacheFromDisk } from '@/lib/query-cache-persistence'
import { resetOnboardingState } from '@/composables/useOnboarding'
import { ONBOARDING_KEYS } from '@/pages/onboarding/constants'
import { resetOnboardingSession } from '@/pages/onboarding/session'
import { safeLocalRemove } from '@/utils/safe-storage'

export interface UserInfo {
  id: string;
  username: string;
  role: string;
  displayName: string;
  avatarUrl: string;
  timezone: string;
}

// The account an access token was issued to, read from its payload (the server
// resolves `user_id` before `sub`, see internal/auth/jwt.go). Only used to match
// locally remembered facts to the signed-in account; the server still verifies
// the token on every request. Empty when the token cannot be read.
function tokenUserId(token: string): string {
  const payload = token.split('.')[1]
  if (!payload) return ''
  try {
    const claims = JSON.parse(atob(payload.replace(/-/g, '+').replace(/_/g, '/'))) as Record<string, unknown>
    const id = claims.user_id || claims.sub
    return typeof id === 'string' ? id.trim() : ''
  } catch {
    return ''
  }
}

// Accounts whose completed onboarding this browser keeps; older ones drop off.
const REMEMBERED_ACCOUNTS = 5

export const useUserStore = defineStore(
  'user',
  () => {
    const userInfo = reactive<UserInfo>({
      id: '',
      username: '',
      role: '',
      displayName: '',
      avatarUrl: '',
      timezone: 'UTC',
    })

    const localToken = useLocalStorage('token', '')
    const onboardingCompleted = ref(false)
    // Ids of the accounts (most recent first, a few at most) whose completed
    // onboarding this browser has seen. They sit next to the token rather than
    // in the persisted store state so they are readable while the store is
    // still being set up, and one only counts for the token's own account —
    // another account signing in here never inherits it. For the same reason
    // signing out keeps them, so signing back in does not wait on the server
    // either. An id is dropped only when the server reports onboarding
    // incomplete for that account (loadMe).
    const onboardingCompletedUsers = useLocalStorage<string[]>(
      ONBOARDING_KEYS.completedUsers,
      [],
      { serializer: StorageSerializers.object },
    )
    const rememberedCompletedUsers = () => (Array.isArray(onboardingCompletedUsers.value) ? onboardingCompletedUsers.value : [])
    const onboardingRememberedFor = (userId: string) => !!userId && rememberedCompletedUsers().includes(userId)
    const chatExamplesSidebarEntryHidden = ref(false)

    watch(onboardingCompleted, (completed) => {
      const userId = tokenUserId(localToken.value)
      if (!completed || !userId || rememberedCompletedUsers()[0] === userId) return
      onboardingCompletedUsers.value = [userId, ...rememberedCompletedUsers().filter(id => id !== userId)]
        .slice(0, REMEMBERED_ACCOUNTS)
    })

    const chatSelection = useChatSelectionStore()
    watch(() => chatSelection.currentBotId, (botId) => {
      if (botId) chatSelection.rememberBot(tokenUserId(localToken.value), botId)
    })

    let _meChecked = false
    let _pendingFetch: Promise<boolean> | null = null

    async function fetchMe(): Promise<boolean> {
      if (_meChecked) return true
      if (onboardingCompleted.value) {
        _meChecked = true
        return true
      }
      return await loadMe()
    }

    // Always asks the server; concurrent callers share one request.
    function loadMe(): Promise<boolean> {
      if (_pendingFetch) return _pendingFetch
      _pendingFetch = (async (): Promise<boolean> => {
        try {
          const { data } = await getUsersMe({ throwOnError: true })
          if (!data) {
            return false
          }
          userInfo.id = data.id ?? ''
          userInfo.username = data.username ?? ''
          userInfo.role = data.role ?? ''
          userInfo.displayName = data.display_name ?? ''
          userInfo.avatarUrl = data.avatar_url ?? ''
          userInfo.timezone = data.timezone || 'UTC'
          const completed = data.metadata?.onboarding_completed === true
          onboardingCompleted.value = completed
          if (!completed && data.id && onboardingRememberedFor(data.id)) {
            onboardingCompletedUsers.value = rememberedCompletedUsers().filter(id => id !== data.id)
          }
          chatExamplesSidebarEntryHidden.value = data.metadata?.chat_examples_sidebar_entry_hidden === true
          _meChecked = true
          return true
        } catch (error) {
          console.warn('Failed to fetch current user:', error)
          return false
        } finally {
          _pendingFetch = null
        }
      })()
      return _pendingFetch
    }

    const resetUserInfo = () => {
      for (const key of Object.keys(userInfo) as (keyof UserInfo)[]) {
        userInfo[key] = key === 'timezone' ? 'UTC' : ''
      }
      chatExamplesSidebarEntryHidden.value = false
    }

    let sidebarEntrySaveToken = 0

    /**
     * Hides or shows the sidebar's usage-example entry for this account, on
     * every device. Applies at once and rolls back if the save fails, unless a
     * newer call has taken over; the error is rethrown for the caller to report.
     */
    async function setChatExamplesSidebarEntryHidden(hidden: boolean): Promise<void> {
      const previous = chatExamplesSidebarEntryHidden.value
      const token = ++sidebarEntrySaveToken
      chatExamplesSidebarEntryHidden.value = hidden
      try {
        await putUsersMe({
          body: { metadata: { chat_examples_sidebar_entry_hidden: hidden } },
          throwOnError: true,
        })
      } catch (error) {
        if (token === sidebarEntrySaveToken) chatExamplesSidebarEntryHidden.value = previous
        throw error
      }
    }

    const clearQueryCache = () => {
      try {
        const queryCache = useQueryCache()
        queryCache.cancelQueries({}, new Error('auth session changed'))
        for (const entry of queryCache.getEntries()) {
          queryCache.remove(entry)
        }
      } catch (error) {
        console.warn('Failed to clear query cache after auth session change:', error)
      }
    }

    const clearFrontendSessionState = (reason: AuthSessionClearReason) => {
      cancelPendingQueryCacheSave()
      clearQueryCache()
      notifyAuthSessionCleared(reason)
      removeQueryCacheFromDisk()
    }

    const login = (userData: UserInfo, token: string) => {
      clearFrontendSessionState('login')
      // A new login must not inherit the previous user's in-memory onboarding
      // state, even across a switch that skips logout. Only what this browser
      // remembers for the signing-in account carries over.
      resetOnboarding()
      chatExamplesSidebarEntryHidden.value = false
      localToken.value = token
      for (const key of Object.keys(userData) as (keyof UserInfo)[]) {
        userInfo[key] = userData[key]
      }
      trustRememberedOnboarding()
      // Open the account's last bot so the chat renders without waiting for
      // the bot list. Deferred one tick so the cleared selection is seen as
      // cleared first; restoring the same bot within the same tick would look
      // like no change and skip the chat store's re-initialization.
      void nextTick(() => {
        if (localToken.value === token) chatSelection.restoreBot(tokenUserId(token))
      })
    }

    const patchUserInfo = (patch: Partial<UserInfo>) => {
      for (const key of Object.keys(patch) as (keyof UserInfo)[]) {
        const value = patch[key]
        if (value !== undefined) {
          userInfo[key] = value
        }
      }
    }

    const resetOnboarding = () => {
      onboardingCompleted.value = false
      _meChecked = false
      _pendingFetch = null
      safeLocalRemove(ONBOARDING_KEYS.introSeen)
      resetOnboardingSession()
      resetOnboardingState()
    }

    const exitLogin = () => {
      clearFrontendSessionState('logout')
      localToken.value = ''
      resetOnboarding()
      resetUserInfo()
    }

    const router = useRouter()
    watch(
      localToken,
      () => {
        if (!localToken.value) {
          clearFrontendSessionState('token-cleared')
          resetOnboarding()
          resetUserInfo()
          if (router.currentRoute.value.name !== 'Login') {
            void router.replace({ name: 'Login' })
          }
        }
      },
      {
        immediate: true,
      },
    )
    onAuthSessionCleared(({ reason }) => {
      if (reason !== 'unauthorized') return
      clearQueryCache()
      localToken.value = ''
      resetOnboarding()
      resetUserInfo()
    })

    // A returning account whose completion this browser has already seen opens
    // the app at once, on a cold start or right after signing in, instead of
    // waiting on /users/me before the first render. The server is still asked;
    // only its confirmed "not completed" sends the user to onboarding. A failed
    // request keeps the remembered answer, the same way an unreachable server
    // never blocks the app below.
    function trustRememberedOnboarding() {
      const signedInUserId = tokenUserId(localToken.value)
      if (!onboardingRememberedFor(signedInUserId)) return
      onboardingCompleted.value = true
      void loadMe().then((fetched) => {
        if (!fetched || onboardingCompleted.value) return
        if (tokenUserId(localToken.value) !== signedInUserId) return
        if (router.currentRoute.value.path !== '/onboarding') {
          void router.replace({ path: '/onboarding' })
        }
      })
    }
    trustRememberedOnboarding()

    return {
      userInfo,
      onboardingCompleted,
      chatExamplesSidebarEntryHidden,
      setChatExamplesSidebarEntryHidden,
      fetchMe,
      login,
      patchUserInfo,
      exitLogin,
    }
  },
  {
    persist: {
      // `onboardingCompleted` stays out: a bare boolean says nothing about
      // which account it belongs to. The remembered completion is the
      // account-keyed `onboardingCompletedUsers` above.
      pick: ['userInfo'],
    },
  },
)
