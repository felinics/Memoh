<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ActionCard, Badge } from '@felinic/ui'
import { chatExampleIcon } from '@/lib/chat-examples/icons'
import { localizeText } from '@/lib/chat-examples/select'
import type { BotCapability, ChatExample } from '@/lib/chat-examples/types'

/**
 * One usage example as an entry card: icon, title and the first line of the
 * prompt. When the bot lacks a capability the example needs, the trailing
 * chevron is replaced by a "Needs …" badge naming the first missing one.
 * The card always sits on a card-colored or editor surface, so it keeps its
 * edge in dark mode too.
 */
const props = defineProps<{
  example: ChatExample
  missing: readonly BotCapability[]
}>()

const emit = defineEmits<{ select: [] }>()

const { t, locale } = useI18n()
const icon = computed(() => chatExampleIcon(props.example.icon))
const title = computed(() => localizeText(props.example.title, locale.value))
const prompt = computed(() => localizeText(props.example.prompt, locale.value))
const missingLabel = computed(() => {
  const capability = props.missing[0]
  return capability ? t('chatExamples.needs', { capability: t(`chatExamples.capability.${capability}`) }) : ''
})
</script>

<template>
  <ActionCard
    as="button"
    type="button"
    bordered
    :title="title"
    :description="prompt"
    @click="emit('select')"
  >
    <template #icon>
      <component :is="icon" />
    </template>
    <template
      v-if="missingLabel"
      #trailing
    >
      <Badge
        variant="secondary"
        size="sm"
      >
        {{ missingLabel }}
      </Badge>
    </template>
  </ActionCard>
</template>
