import { defineStore } from 'pinia'
import { reactive, ref, watch } from 'vue'
import { useLocalStorage } from '@vueuse/core'
import { useRouter } from 'vue-router'
import { useQueryCache } from '@pinia/colada'
import { getUsersMe, putUsersMe } from '@memohai/sdk'
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
    const chatExamplesSidebarEntryHidden = ref(false)

    let _meChecked = false
    let _pendingFetch: Promise<boolean> | null = null

    async function fetchMe(): Promise<boolean> {
      if (_meChecked) return true
      if (onboardingCompleted.value) {
        _meChecked = true
        return true
      }
      if (_pendingFetch) return await _pendingFetch
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
          onboardingCompleted.value = data.metadata?.onboarding_completed === true
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
      return await _pendingFetch
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
      // A new login must not inherit the previous user's onboarding state. The
      // flag is no longer persisted, but an in-memory value from a prior user
      // could otherwise leak across a switch that skips logout — force a fresh
      // server check.
      resetOnboarding()
      chatExamplesSidebarEntryHidden.value = false
      localToken.value = token
      for (const key of Object.keys(userData) as (keyof UserInfo)[]) {
        userInfo[key] = userData[key]
      }
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
      // Do NOT persist `onboardingCompleted`: a localStorage value can be stale
      // across user switches or pushed between machines by browser sync, which
      // would skip onboarding for the wrong user. It is verified against the
      // server once per session via fetchMe() and cached in memory (_meChecked).
      pick: ['userInfo'],
    },
  },
)
