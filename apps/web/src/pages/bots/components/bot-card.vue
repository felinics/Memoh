<template>
  <PersonaTile
    :name="bot.display_name || bot.id"
    :disabled="isPending"
    :aria-label="`${bot.display_name || bot.id}`"
    @click="onOpenDetail"
  >
    <!-- Corner status: present only when the bot needs attention. A healthy,
         active bot renders nothing here. -->
    <template #status>
      <LoaderCircle
        v-if="isPending"
        class="size-4 animate-spin text-muted-foreground"
      />
      <span
        v-else-if="hasIssue"
        class="flex items-center text-destructive"
        :title="issueTitle"
      >
        <AlertTriangle class="size-4" />
      </span>
      <span
        v-else-if="!bot.is_active"
        class="size-2 rounded-full bg-muted-foreground/40"
        :title="$t('bots.inactive')"
      />
    </template>

    <template #media>
      <Avatar
        class="size-14"
        :class="{ 'opacity-60': !bot.is_active && !isPending }"
      >
        <AvatarImage
          v-if="bot.avatar_url"
          :src="bot.avatar_url"
          :alt="bot.display_name"
        />
        <AvatarFallback class="text-base">
          {{ avatarFallback }}
        </AvatarFallback>
      </Avatar>
    </template>
  </PersonaTile>
</template>

<script setup lang="ts">
import {
  Avatar,
  AvatarImage,
  AvatarFallback,
} from '@felinic/ui'
import { AlertTriangle, LoaderCircle } from 'lucide-vue-next'
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import type { BotsBot } from '@memohai/sdk'
import { PersonaTile } from '@felinic/ui'
import { useAvatarInitials } from '@/composables/useAvatarInitials'
import { useBotStatusMeta } from '@/composables/useBotStatusMeta'

const router = useRouter()
const { t } = useI18n()

const props = defineProps<{
  bot: BotsBot
}>()

const botRef = computed(() => props.bot)

const avatarFallback = useAvatarInitials(() => props.bot.display_name || props.bot.id)

const { hasIssue, isPending, issueTitle } = useBotStatusMeta(botRef, t)

function onOpenDetail() {
  if (isPending.value) return
  router.push({ name: 'bot-detail', params: { botName: props.bot.name ?? props.bot.id } })
}
</script>
