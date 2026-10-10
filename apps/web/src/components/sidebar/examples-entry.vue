<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Lightbulb } from 'lucide-vue-next'
import { useChatExamplesUi } from '@/components/chat-examples/use-chat-examples-ui'
import SidebarNavButton from './nav-button.vue'

/**
 * The sidebar's entry to the usage-example gallery, shared by the desktop
 * rail and the mobile nav sheet. It stays enabled without a bot: picking an
 * example then explains that a bot is needed and offers to create one.
 */
const { t } = useI18n()
const { openGallery } = useChatExamplesUi()
</script>

<template>
  <SidebarNavButton @click="openGallery">
    <Lightbulb
      :stroke-width="1.75"
      class="size-[18px]"
    />
    {{ t('chatExamples.sidebarEntry') }}
  </SidebarNavButton>
</template>
