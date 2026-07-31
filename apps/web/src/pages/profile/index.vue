<template>
  <PageShell :title="$t('sidebar.profile')">
    <div class="space-y-8">
      <!-- Stay-local (not SettingsRow): skeleton screens have no owner yet —
           skeleton-shimmer isn't on the four-rung loading ladder. These rows
           borrow SettingsRow's geometry (mx-4/border-b/py-3.5/last:border-b-0)
           only so the real content doesn't reflow once it swaps in. -->
      <template v-if="loadingInitial">
        <div class="overflow-hidden rounded-[var(--radius-menu-shell)] border border-border bg-card">
          <div
            v-for="i in 6"
            :key="i"
            class="mx-4 flex items-center justify-between border-b border-border py-3.5 last:border-b-0"
          >
            <Skeleton class="h-4 w-24" />
            <Skeleton class="h-8 w-64" />
          </div>
        </div>
      </template>

      <template v-else>
        <!-- Card 1 · Profile: avatar + display name (hover-to-edit) only -->
        <SettingsSection>
          <ProfileIdentity
            :avatar-url="profileForm.avatar_url"
            :display-name="profileForm.display_name"
            :username="displayUsername"
            :fallback="avatarFallback"
            @update:avatar-url="profileForm.avatar_url = $event"
            @update:display-name="profileForm.display_name = $event"
            @save="autoSaveProfile"
          />
        </SettingsSection>

        <!-- Card 2 · Account: profile-wide preferences + password -->
        <SettingsSection :title="$t('settings.accountSection')">
          <SettingsRow :label="$t('settings.timezone')">
            <div class="w-64">
              <TimezoneSelect
                :model-value="profileForm.timezone"
                :placeholder="$t('settings.timezonePlaceholder')"
                @update:model-value="onTimezoneChange"
              />
            </div>
          </SettingsRow>

          <SettingsRow
            :label="$t('settings.titleModel')"
            :description="$t('settings.titleModelDescription')"
          >
            <div class="w-64">
              <ModelSelect
                v-model="profileForm.title_model_id"
                :models="models"
                :providers="providers"
                model-type="chat"
                :placeholder="$t('settings.titleModelPlaceholder')"
                :none-label="$t('settings.titleModelPlaceholder')"
                @update:model-value="onTitleModelChange"
              />
            </div>
          </SettingsRow>

          <SettingsRow :label="$t('auth.password')">
            <PasswordSection
              :open="passwordDialogOpen"
              :saving="savingPassword"
              @update:open="passwordDialogOpen = $event"
              @submit="onSubmitPassword"
            />
          </SettingsRow>
        </SettingsSection>

        <!-- Connected IM Accounts -->
        <ConnectedAccountsSection />

        <!-- Card 3 · Session: user id + sign out (low-frequency, kept at the bottom). -->
        <SettingsSection :title="$t('settings.sessionSection')">
          <SettingsRow :label="$t('settings.userID')">
            <div class="flex items-center gap-1 text-sm text-muted-foreground">
              <span class="max-w-[16rem] truncate font-mono text-xs">{{ displayUserID }}</span>
              <Button
                variant="ghost"
                size="icon-sm"
                :aria-label="$t('common.copy')"
                @click="copyToClipboard(displayUserID)"
              >
                <Check
                  v-if="copiedId"
                  class="size-3.5"
                />
                <Copy
                  v-else
                  class="size-3.5"
                />
              </Button>
            </div>
          </SettingsRow>

          <SettingsRow
            v-if="canSignOut"
            :label="$t('auth.logout')"
          >
            <ConfirmPopover
              :message="$t('auth.logoutConfirm')"
              :cancel-text="$t('common.cancel')"
              :confirm-text="$t('common.confirm')"
              @confirm="onLogout"
            >
              <template #trigger>
                <Button
                  variant="outline"
                  size="sm"
                >
                  {{ $t('auth.logout') }}
                </Button>
              </template>
            </ConfirmPopover>
          </SettingsRow>
        </SettingsSection>
      </template>
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { useRouter } from 'vue-router'
import { ConfirmPopover, PageShell, SettingsRow, SettingsSection, toast, useClipboard } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { Button, Skeleton } from '@felinic/ui'
import { Check, Copy } from 'lucide-vue-next'

import TimezoneSelect from '@/components/timezone-select/index.vue'
import ModelSelect from '@/pages/bots/components/model-select.vue'
import ProfileIdentity from './components/profile-identity.vue'
import PasswordSection from './components/password-section.vue'
import ConnectedAccountsSection from './components/connected-accounts-section.vue'

import { getModels, getProviders, getUsersMe, putUsersMe, putUsersMePassword } from '@memohai/sdk'
import type { AccountsAccount, AccountsUpdateProfileRequest, AccountsUpdatePasswordRequest } from '@memohai/sdk'
import { useUserStore } from '@/store/user'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useAvatarInitials } from '@/composables/useAvatarInitials'

type UserAccount = AccountsAccount

const { t } = useI18n()
const { copyText } = useClipboard()
const router = useRouter()
const userStore = useUserStore()
const { userInfo, exitLogin, patchUserInfo } = userStore

const canSignOut = true

// ---- User data ----
const account = ref<UserAccount | null>(null)

const { data: modelData } = useQuery({
  key: ['all-models'],
  query: async () => {
    const { data } = await getModels({ throwOnError: true })
    return data
  },
})

const { data: providerData } = useQuery({
  key: ['all-providers'],
  query: async () => {
    const { data } = await getProviders({ throwOnError: true })
    return data
  },
})

const models = computed(() => modelData.value ?? [])
const providers = computed(() => providerData.value ?? [])

const loadingInitial = ref(true)
const savingPassword = ref(false)

const originalProfile = reactive({
  display_name: '',
  avatar_url: '',
  timezone: '',
  title_model_id: '',
})

const profileForm = reactive({
  display_name: '',
  avatar_url: '',
  timezone: '',
  title_model_id: '',
})

const passwordDialogOpen = ref(false)
const copiedId = ref(false)

async function copyToClipboard(text: string) {
  const ok = await copyText(text)
  if (!ok) {
    toast.error(t('common.copyFailed'))
    return
  }
  copiedId.value = true
  setTimeout(() => copiedId.value = false, 2000)
}

const displayUserID = computed(() => account.value?.id || userInfo.id || '')
const displayUsername = computed(() => account.value?.username || userInfo.username || '')
const displayTitle = computed(() => {
  return profileForm.display_name.trim() || displayUsername.value || displayUserID.value || t('settings.user')
})
const avatarFallback = useAvatarInitials(() => displayTitle.value, 'U')

onMounted(() => {
  void loadPageData()
})

async function loadPageData() {
  loadingInitial.value = true
  try {
    await loadMyAccount()
  } catch {
    toast.error(t('settings.loadUserFailed'))
  } finally {
    loadingInitial.value = false
  }
}

async function loadMyAccount() {
  const { data } = await getUsersMe({ throwOnError: true })
  account.value = data
  
  const dName = data.display_name || ''
  const aUrl = data.avatar_url || ''
  const tZone = data.timezone || 'UTC'
  const titleModelID = data.title_model_id || ''

  // Set forms
  profileForm.display_name = dName
  profileForm.avatar_url = aUrl
  profileForm.timezone = tZone
  profileForm.title_model_id = titleModelID
  
  // Set originals
  originalProfile.display_name = dName
  originalProfile.avatar_url = aUrl
  originalProfile.timezone = tZone
  originalProfile.title_model_id = titleModelID

  patchUserInfo({
    id: data.id,
    username: data.username,
    role: data.role,
    displayName: dName,
    avatarUrl: aUrl,
    timezone: tZone,
  })
}

function onTimezoneChange(value: string | number | undefined) {
  profileForm.timezone = String(value || 'UTC')
  void autoSaveProfile()
}

function onTitleModelChange(value: string | undefined) {
  profileForm.title_model_id = value || ''
  void autoSaveProfile()
}

// Monotonic token so out-of-order PUT responses can't clobber newer state: each
// save claims a token, and only the latest dispatch is allowed to apply its
// result or roll back on failure.
let saveToken = 0

// Silent auto-save: triggered on name confirm, preference changes, and avatar apply.
// Skips the request when nothing actually changed; only surfaces errors.
async function autoSaveProfile() {
  const body: AccountsUpdateProfileRequest = {
    display_name: profileForm.display_name.trim(),
    avatar_url: profileForm.avatar_url.trim(),
    timezone: profileForm.timezone.trim(),
    title_model_id: profileForm.title_model_id.trim(),
  }
  if (
    body.display_name === originalProfile.display_name
    && body.avatar_url === originalProfile.avatar_url
    && body.timezone === originalProfile.timezone
    && body.title_model_id === originalProfile.title_model_id
  ) {
    return
  }

  const token = ++saveToken
  try {
    const { data } = await putUsersMe({ body, throwOnError: true })
    // A newer save has since been dispatched — let it own the final state.
    if (token !== saveToken) return
    account.value = data

    const dName = data.display_name || ''
    const aUrl = data.avatar_url || ''
    const tZone = data.timezone || 'UTC'
    const titleModelID = data.title_model_id || ''

    profileForm.display_name = dName
    profileForm.avatar_url = aUrl
    profileForm.timezone = tZone
    profileForm.title_model_id = titleModelID

    originalProfile.display_name = dName
    originalProfile.avatar_url = aUrl
    originalProfile.timezone = tZone
    originalProfile.title_model_id = titleModelID

    patchUserInfo({
      displayName: dName,
      avatarUrl: aUrl,
      timezone: tZone,
    })
  } catch (error) {
    // Roll back the optimistic local edit so the UI re-matches the server —
    // but only if no newer save superseded this one (which would own state).
    if (token === saveToken) {
      profileForm.display_name = originalProfile.display_name
      profileForm.avatar_url = originalProfile.avatar_url
      profileForm.timezone = originalProfile.timezone
      profileForm.title_model_id = originalProfile.title_model_id
      patchUserInfo({
        displayName: originalProfile.display_name,
        avatarUrl: originalProfile.avatar_url,
        timezone: originalProfile.timezone,
      })
    }
    toast.error(resolveApiErrorMessage(error, t('settings.profileUpdateFailed'), { prefixFallback: true }))
  }
}

async function onSubmitPassword(payload: { currentPassword: string, newPassword: string }) {
  const currentPassword = payload.currentPassword.trim()
  const newPassword = payload.newPassword.trim()
  if (!currentPassword || !newPassword) {
    toast.error(t('settings.passwordRequired'))
    return
  }
  savingPassword.value = true
  try {
    const body: AccountsUpdatePasswordRequest = {
      current_password: currentPassword,
      new_password: newPassword,
    }
    await putUsersMePassword({ body, throwOnError: true })
    passwordDialogOpen.value = false
    toast.success(t('settings.passwordUpdated'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('settings.passwordUpdateFailed'), { prefixFallback: true }))
  } finally {
    savingPassword.value = false
  }
}

function onLogout() {
  exitLogin()
  void router.replace({ name: 'Login' })
}
</script>
