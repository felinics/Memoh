<template>
  <Select
    :model-value="modelValue"
    @update:model-value="$emit('update:modelValue', $event as string)"
  >
    <SelectTrigger :class="triggerClass">
      <SelectValue :placeholder="placeholder || $t('supermarket.selectBotPlaceholder')">
        <div
          v-if="selectedBot"
          class="flex items-center gap-2"
        >
          <Avatar class="size-5 shrink-0">
            <AvatarImage
              v-if="selectedBot.avatar_url"
              :src="selectedBot.avatar_url"
              :alt="selectedBot.display_name"
            />
            <AvatarFallback class="text-[9px]">
              {{ initials(selectedBot.display_name || selectedBot.id || '') }}
            </AvatarFallback>
          </Avatar>
          <span class="truncate text-xs">{{ selectedBot.display_name || selectedBot.id }}</span>
        </div>
      </SelectValue>
    </SelectTrigger>
    <SelectContent>
      <SelectItem
        v-for="bot in bots"
        :key="bot.id"
        :value="bot.id!"
      >
        <div class="flex items-center gap-2">
          <Avatar class="size-5 shrink-0">
            <AvatarImage
              v-if="bot.avatar_url"
              :src="bot.avatar_url"
              :alt="bot.display_name"
            />
            <AvatarFallback class="text-[9px]">
              {{ initials(bot.display_name || bot.id || '') }}
            </AvatarFallback>
          </Avatar>
          <span class="truncate text-xs">{{ bot.display_name || bot.id }}</span>
        </div>
      </SelectItem>
    </SelectContent>
  </Select>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useQuery } from '@pinia/colada'
import {
  Select, SelectTrigger, SelectValue, SelectContent, SelectItem,
  Avatar, AvatarImage, AvatarFallback,
} from '@felinic/ui'
import { getBotsQuery } from '@memohai/sdk/colada'
import type { BotsBot } from '@memohai/sdk'

const props = defineProps<{
  modelValue: string
  placeholder?: string
  triggerClass?: string
}>()

defineEmits<{
  'update:modelValue': [value: string]
}>()

const { data: botsData } = useQuery(getBotsQuery())
const bots = computed<BotsBot[]>(() => botsData.value?.items ?? [])

const selectedBot = computed(() =>
  bots.value.find((b) => b.id === props.modelValue),
)

function initials(name: string): string {
  return name
    .split(/[\s_-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0])
    .join('')
    .toUpperCase()
}
</script>
