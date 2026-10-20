<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Lightbulb, X } from 'lucide-vue-next'
import { Avatar, AvatarFallback, Button, toast } from '@felinic/ui'
import { useChatExamplesUi } from '@/components/chat-examples/use-chat-examples-ui'
import SidebarNavButton from './nav-button.vue'

/**
 * The sidebar's entry to the usage-example gallery, shared by the desktop
 * rail and the mobile nav sheet. It stays enabled without a bot: picking an
 * example then explains that a bot is needed and offers to create one.
 *
 * The trailing × hides the entry for the account; the toast it raises names
 * the Appearance switch that brings it back and offers an undo. It is taken
 * down again if the save fails, so it never contradicts the failure toast.
 *
 * `besideAccount` mirrors the account row it sits on top of (user-menu.vue):
 * the same h-10 row, a 26px avatar-shaped tile on the avatar's column, and the
 * label on the user name's column. Without it the entry is a plain sidebar
 * row, matching the Settings row next to it in the mobile sheet.
 */
withDefaults(defineProps<{ besideAccount?: boolean }>(), { besideAccount: false })

const { t } = useI18n()
const { openGallery, setSidebarEntryHidden } = useChatExamplesUi()

async function hideEntry() {
  const hiddenToast = toast(t('chatExamples.sidebarEntryHidden'), {
    action: {
      label: t('chatExamples.undoHideSidebarEntry'),
      onClick: () => { void setSidebarEntryHidden(false) },
    },
  })
  if (!(await setSidebarEntryHidden(true))) toast.dismiss(hiddenToast)
}
</script>

<template>
  <div class="group relative">
    <SidebarNavButton
      :class="besideAccount ? 'h-10 pr-9' : 'pr-9'"
      @click="openGallery"
    >
      <Avatar
        v-if="besideAccount"
        class="size-[26px] shrink-0"
      >
        <AvatarFallback>
          <Lightbulb
            :stroke-width="1.75"
            class="size-3.5"
          />
        </AvatarFallback>
      </Avatar>
      <Lightbulb
        v-else
        :stroke-width="1.75"
        class="size-[18px]"
      />
      <span class="min-w-0 flex-1 truncate text-left">{{ t('chatExamples.sidebarEntry') }}</span>
    </SidebarNavButton>
    <!-- quiet, not ghost: a ghost chip's hover gray matches the hovered row
         behind it, so only the glyph brightens. Hover-revealed on pointers;
         always shown on touch, where there is no hover to reveal it. -->
    <Button
      variant="quiet"
      size="icon-sm"
      class="absolute inset-y-0 right-1 my-auto opacity-0 transition-opacity duration-150 group-hover:opacity-100 focus-visible:opacity-100 pointer-coarse:opacity-100"
      :aria-label="t('chatExamples.hideSidebarEntry')"
      @click="hideEntry"
    >
      <X class="size-4" />
    </Button>
  </div>
</template>
