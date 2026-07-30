<template>
  <!-- One memory entry as a SettingsRow inside the date/search section card.
       Composing the owner (instead of a standalone bordered card) is what keeps
       the stream hairline-separated and kills the card-in-card nesting — the
       section card is the only frame. -->
  <SettingsRow
    align="center"
    class="group"
  >
    <template #content>
      <p class="text-control leading-snug text-foreground whitespace-pre-wrap wrap-break-word">
        {{ item.memory }}
      </p>

      <!-- Metadata row: layer + confidence + tags badges, then relative time. -->
      <div
        v-if="hasMeta"
        class="mt-2 flex flex-wrap items-center gap-1.5"
      >
        <Badge
          :variant="layerBadgeVariant"
          size="sm"
        >
          {{ $t(`bots.memory.layer.${layer}`) }}
        </Badge>
        <Badge
          v-if="confidence !== null"
          variant="secondary"
          size="sm"
          font="mono"
        >
          {{ confidence.toFixed(2) }}
        </Badge>
        <Badge
          v-for="tag in tags.slice(0, 4)"
          :key="tag"
          variant="outline"
          size="sm"
        >
          {{ tag }}
        </Badge>
        <Badge
          v-if="showScore && typeof item.score === 'number'"
          variant="info"
          size="sm"
          font="mono"
        >
          {{ item.score.toFixed(2) }}
        </Badge>
        <span
          v-if="showTimestamp"
          class="ml-1 text-caption text-muted-foreground"
        >
          {{ formatRelativeTime(item.created_at, { locale }) }}
        </span>
      </div>
    </template>

    <Button
      variant="ghost"
      size="icon-sm"
      class="shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100"
      :aria-label="$t('bots.memory.editMemory')"
      @click="$emit('edit')"
    >
      <Pencil class="size-4" />
    </Button>
  </SettingsRow>
</template>

<script setup lang="ts">
import { Pencil } from 'lucide-vue-next'
import { computed } from 'vue'
import { Badge, Button, SettingsRow } from '@felinic/ui'
import type { AdaptersMemoryItem } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { formatRelativeTime } from '@/utils/date-time'
import { memoryConfidence, memoryLayerOf, memoryTags } from './use-memory-filter'

type BadgeVariant = 'default' | 'secondary' | 'destructive' | 'success' | 'warning' | 'info' | 'outline'

const props = withDefaults(defineProps<{
  item: AdaptersMemoryItem & { id?: string; memory: string }
  locale: string
  showScore?: boolean
  /** Dated stream groups already show the day in the section title. */
  showTimestamp?: boolean
}>(), {
  showScore: false,
  showTimestamp: true,
})

defineEmits<{ edit: [] }>()

const { t: _t } = useI18n()

const layer = computed(() => memoryLayerOf(props.item))
const confidence = computed(() => memoryConfidence(props.item))
const tags = computed(() => memoryTags(props.item))

const layerBadgeVariant = computed<BadgeVariant>(() => {
  switch (layer.value) {
    case 'identity': return 'info'
    case 'preference': return 'success'
    case 'context': return 'warning'
    case 'experience': return 'default'
    case 'activity': return 'secondary'
    default: return 'outline'
  }
})

const hasMeta = computed(() =>
  confidence.value !== null
  || tags.value.length > 0
  || (props.showScore && typeof props.item.score === 'number')
  || (props.showTimestamp && Boolean(props.item.created_at)),
)
</script>
