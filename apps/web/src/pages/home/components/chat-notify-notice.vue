<template>
  <!-- A run's notify_user call, in the text flow. It is what the user was
       pointed at, so it takes the same quiet card as a completed ask_user
       (chat-answers-card) rather than the muted process capsule. One line,
       wrapping to two only when the column is narrow; width hugs the copy. -->
  <SurfaceCard
    data-slot="notify-notice"
    padding="row"
    class="w-fit max-w-full"
  >
    <p class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm">
      <span class="inline-flex items-center gap-1.5 font-medium text-foreground">
        <BellRing
          class="size-4 shrink-0 text-brand"
          aria-hidden="true"
        />
        {{ t('chat.notifyNotice.title') }}
      </span>
      <span class="text-muted-foreground">{{ t('chat.notifyNotice.description') }}</span>
    </p>
  </SurfaceCard>
</template>

<script setup lang="ts">
import { SurfaceCard } from '@felinic/ui'
import { BellRing } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
</script>
