<template>
  <!-- Footer account row: avatar + name, opening the consolidated menu
       (Usage / About / Settings | Members / Log Out) that absorbs the old
       footer Settings button. The trigger IS the sidebar row owner
       (SidebarNavButton), so hover chrome comes from the design system
       instead of a hand-written fill; the scoped style below only retimes it. -->
  <DropdownMenu v-model:open="menuOpen">
    <DropdownMenuTrigger as-child>
      <!-- h-10 (2px over the shared h-9 per side): gives the 26px avatar 7px
           vertical insets against the 11px left inset — close enough to
           equidistant without changing the footer rhythm. twMerge keeps this
           later class over the owner's h-9. -->
      <SidebarNavButton
        class="user-menu-trigger h-10"
        :aria-label="t('sidebar.userMenu')"
      >
        <!-- 26px avatar (up from the switcher's 22): center at 10+11+13=34px
             from the panel edge, matched by the menu's alignOffset below. -->
        <Avatar class="size-[26px] shrink-0">
          <AvatarImage
            v-if="userInfo.avatarUrl"
            :src="userInfo.avatarUrl"
            :alt="label"
          />
          <AvatarFallback :class="avatarFallbackClass">
            {{ initials }}
          </AvatarFallback>
        </Avatar>
        <span class="min-w-0 flex-1 truncate text-left">{{ label }}</span>
      </SidebarNavButton>
    </DropdownMenuTrigger>
    <!-- Faster than the library default (100/120ms): this menu opens on every
         account interaction, so it gets a snappier 75ms both ways. Width and
         shadow tuning live in menuContentClass below (with its rationale).
         alignOffset 2: the menu's icon column center sits 22px in from the
         content's left edge (measured: border + frame + row inset + icon
         half), and the 26px avatar's center is 10+11+13=34px from the sidebar
         edge — offsetting the content 2px right puts both on x=34. If the
         avatar size or footer pad changes, shift this offset by the same
         delta the avatar center moved. -->
    <DropdownMenuContent
      side="top"
      align="start"
      :align-offset="2"
      :class="menuContentClass"
    >
      <DropdownMenuItem
        class="gap-2"
        @select="router.push({ name: 'usage' })"
      >
        <UsageIcon />
        <span>{{ t('sidebar.usage') }}</span>
        <!-- Trailing quota text is Cloud-only: the bridge is absent on OSS and
             null on any fetch failure, and the row then stays a plain label. -->
        <span
          v-if="usedPercent !== null"
          class="ml-auto text-body text-muted-foreground"
        >{{ usedPercent }}%</span>
      </DropdownMenuItem>
      <DropdownMenuItem
        class="gap-2"
        @select="router.push({ name: 'about' })"
      >
        <InfoIcon />
        <span>{{ t('sidebar.about') }}</span>
      </DropdownMenuItem>
      <DropdownMenuItem
        class="gap-2"
        @select="router.push('/settings')"
      >
        <SettingsIcon />
        <span>{{ t('sidebar.settings') }}</span>
      </DropdownMenuItem>
      <DropdownMenuSeparator />
      <DropdownMenuItem
        v-if="userInfo.role?.toLowerCase() === 'admin'"
        class="gap-2"
        @select="router.push({ name: 'people' })"
      >
        <UsersIcon />
        <span>{{ t('sidebar.people') }}</span>
      </DropdownMenuItem>
      <DropdownMenuItem
        class="gap-2"
        @select="onSignOut"
      >
        <LogoutIcon />
        <span>{{ t('auth.logout') }}</span>
      </DropdownMenuItem>
    </DropdownMenuContent>
  </DropdownMenu>
</template>

<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import {
  Avatar,
  AvatarImage,
  AvatarFallback,
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@felinic/ui'
import {
  InfoIcon,
  LogoutIcon,
  SettingsIcon,
  UsageIcon,
  UsersIcon,
} from '@memohai/icon/ui'
import { useUserStore } from '@/store/user'
import { avatarInitials } from '@/composables/useAvatarInitials'
import { UsageQuotaKey } from '@/lib/usage-quota'
import SidebarNavButton from './nav-button.vue'

const router = useRouter()
const { t } = useI18n()
const userStore = useUserStore()
const { userInfo } = storeToRefs(userStore)

const menuOpen = ref(false)

const label = computed(() => userInfo.value.displayName || userInfo.value.username || '—')
const initials = computed(() => avatarInitials(label.value, '?'))
// Fallback glyph size tracks the footer avatar (26px); kept as a script
// constant so the guard marker has a home.
const avatarFallbackClass = 'text-[10px]' /* ui-allow-px: 26px avatar fallback initials */

// w-52 (208px) ≈ trigger row width — w-56 floated visibly wider than the
// footer. Shadow: --shadow-dropdown (3%/5% alpha) reads flat over the
// sidebar's own light surface, so this instance gets a faint-but-present lift
// (4%/8%). Token-level tuning belongs to the ui package; pending that, keep
// the divergence local to this menu.
const menuContentClass = 'user-menu-content w-52 duration-75 data-[state=closed]:duration-75 shadow-[0_1px_2px_oklch(0_0_0/0.04),0_8px_24px_-8px_oklch(0_0_0/0.08)]' /* ui-allow-style: one-off lift over --shadow-dropdown — token tuning is ui-repo follow-up */

// Quota summary for the Usage row — injected only by the hosted distribution;
// refreshed on each open so the number is never stale, silent on failure.
const quota = inject(UsageQuotaKey, undefined)
const usedPercent = ref<number | null>(null)
watch(menuOpen, async (open) => {
  if (!open || !quota) return
  try {
    const percent = await quota.getUsedPercent()
    usedPercent.value = typeof percent === 'number' && Number.isFinite(percent)
      ? Math.max(0, Math.min(100, Math.round(percent)))
      : null
  } catch {
    usedPercent.value = null
  }
})

// Sign-out is cheap and fully reversible (the profile page keeps its own
// confirm step); the menu's two-click path is enough ceremony for a menu row.
function onSignOut() {
  userStore.exitLogin()
  void router.replace({ name: 'Login' })
}
</script>

<style scoped>
/* Pointer-honest trigger chrome. The ghost ::before fill fades over 150ms by
   design-system default; here the hover chip appears/disappears instantly
   (transition kept only for the press scale), and the menu-open state must not
   pin the fill while the pointer has moved onto the panel — the row highlight
   follows the real pointer, not the menu state. */
.user-menu-trigger::before {
  transition-property: scale;
}
.user-menu-trigger[data-state='open']:not(:hover)::before {
  background-color: transparent;
}
</style>
