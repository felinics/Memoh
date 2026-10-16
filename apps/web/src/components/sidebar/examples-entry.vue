<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Lightbulb } from 'lucide-vue-next'
import { Avatar, AvatarFallback } from '@felinic/ui'
import { useChatExamplesUi } from '@/components/chat-examples/use-chat-examples-ui'
import SidebarNavButton from './nav-button.vue'

/**
 * The sidebar's entry to the usage-example gallery, shared by the desktop
 * rail and the mobile nav sheet. It stays enabled without a bot: picking an
 * example then explains that a bot is needed and offers to create one.
 *
 * `besideAccount` mirrors the account row it sits on top of (user-menu.vue):
 * the same h-10 row, a 26px avatar-shaped tile on the avatar's column, and the
 * label on the user name's column. Without it the entry is a plain sidebar
 * row, matching the Settings row next to it in the mobile sheet.
 */
withDefaults(defineProps<{ besideAccount?: boolean }>(), { besideAccount: false })

const { t } = useI18n()
const { openGallery } = useChatExamplesUi()
</script>

<template>
  <SidebarNavButton
    :class="besideAccount ? 'h-10' : undefined"
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
</template>
