<script setup lang="ts">
import { reactive, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Checkbox } from '@felinic/ui'
import {
  ChevronDown,
  TriangleAlert,
  SlidersHorizontal,
  Boxes,
  ShieldAlert,
  MessageSquare,
  Link as LinkIcon,
  Clock,
  Mail,
  MessageCircle,
  Paperclip,
  HardDrive,
  type LucideIcon,
} from 'lucide-vue-next'

type SectionState = 'skip' | 'merge' | 'replace'

interface SectionItem {
  key: string
  count?: number
  targetCount?: number
  conflict?: boolean
  sensitive?: boolean
  items?: string[]
  // When false, the section is shown disabled (e.g. not in the backup).
  available?: boolean
}

const props = defineProps<{
  sections: SectionItem[]
  // include: card is an include/exclude toggle (merge|skip)
  // strategy: card offers skip/merge/replace (overwrite imports)
  mode: 'include' | 'strategy'
  modelValue: Record<string, SectionState>
  disabled?: boolean
  // Text shown on a disabled (unavailable) section. Differs by context:
  // export = "no data to export", import = "not in backup".
  emptyText?: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, SectionState>]
}>()

const { t } = useI18n()

const emptyLabel = computed(() => props.emptyText || t('bots.backup.notInBackup'))

const iconMap: Record<string, LucideIcon> = {
  settings: SlidersHorizontal,
  models: Boxes,
  acl: ShieldAlert,
  channels: MessageSquare,
  mcp: LinkIcon,
  schedules: Clock,
  email: Mail,
  history: MessageCircle,
  assets: Paperclip,
  workspace: HardDrive,
}

const strategyOptions = (key: string): SectionState[] =>
  key === 'workspace' || key === 'settings'
    ? ['skip', 'merge']
    : ['skip', 'merge', 'replace']

const expanded = reactive<Record<string, boolean>>({})

function isAvailable(item: SectionItem) {
  // count < 0 means "available but unknown count" (e.g. workspace on export).
  return item.available !== false && (item.count ?? 0) !== 0
}

function hasDetail(item: SectionItem) {
  return (item.items?.length ?? 0) > 0
}

function toggleExpand(key: string) {
  expanded[key] = !expanded[key]
}

function stateOf(key: string): SectionState {
  return props.modelValue[key] ?? 'skip'
}

function setState(item: SectionItem, state: SectionState) {
  if (props.disabled || !isAvailable(item)) return
  emit('update:modelValue', { ...props.modelValue, [item.key]: state })
}

function toggleInclude(item: SectionItem) {
  setState(item, stateOf(item.key) === 'skip' ? 'merge' : 'skip')
}

function showCount(item: SectionItem) {
  return item.key !== 'settings' && (item.count ?? 0) > 0
}

const visibleSections = computed(() => props.sections.filter(s => iconMap[s.key]))
</script>

<template>
  <div class="space-y-2">
    <div
      v-for="item in visibleSections"
      :key="item.key"
      class="overflow-hidden rounded-md border transition-colors"
      :class="[
        !isAvailable(item)
          ? 'border-border/40 bg-muted/20 opacity-50'
          : mode === 'include' && stateOf(item.key) !== 'skip'
            ? 'border-foreground bg-muted'
            : 'border-border-soft bg-background/50',
      ]"
    >
      <div class="flex items-center gap-3 px-3 py-2.5">
        <!-- Main toggle / info region -->
        <div
          class="flex min-w-0 flex-1 items-center gap-3"
          :class="[
            mode === 'include' && isAvailable(item) && !disabled ? 'cursor-pointer' : '',
          ]"
          @click="mode === 'include' && toggleInclude(item)"
        >
          <div class="flex size-8 shrink-0 items-center justify-center rounded bg-background border border-border/50 text-muted-foreground">
            <component
              :is="iconMap[item.key]"
              class="size-4"
            />
          </div>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-1.5">
              <span class="text-xs font-medium text-foreground">{{ t(`bots.backup.sections.${item.key}`) }}</span>
              <span
                v-if="showCount(item)"
                class="text-[11px] text-muted-foreground"
              >×{{ item.count }}</span>
              <TriangleAlert
                v-if="item.sensitive"
                class="size-3 text-warning-foreground"
                :aria-label="t('bots.backup.sensitiveHint')"
              />
              <span
                v-if="item.conflict"
                class="h-4 rounded-full border border-warning-border bg-warning-soft px-1.5 text-[9px] font-medium leading-4 text-warning-foreground"
              >{{ t('bots.backup.conflict') }}</span>
            </div>
            <div
              v-if="item.sensitive"
              class="text-[10px] text-warning-foreground"
            >
              {{ t('bots.backup.sensitiveHint') }}
            </div>
            <div
              v-if="mode === 'strategy'"
              class="text-[10px] text-muted-foreground"
            >
              <template v-if="isAvailable(item)">
                {{ t('bots.backup.countBackup', { n: item.count ?? 0 }) }}
                <span v-if="(item.targetCount ?? 0) > 0">· {{ t('bots.backup.countCurrent', { n: item.targetCount }) }}</span>
              </template>
              <template v-else>
                {{ emptyLabel }}
              </template>
            </div>
            <div
              v-else-if="!isAvailable(item)"
              class="text-[10px] text-muted-foreground"
            >
              {{ emptyLabel }}
            </div>
          </div>
        </div>

        <!-- Strategy selector (overwrite) -->
        <div
          v-if="mode === 'strategy' && isAvailable(item)"
          class="flex shrink-0 items-center gap-0.5 rounded-md border border-border-soft bg-background p-0.5"
        >
          <button
            v-for="opt in strategyOptions(item.key)"
            :key="opt"
            type="button"
            :disabled="disabled"
            class="rounded px-1.5 py-0.5 text-[10px] font-medium transition-colors"
            :class="stateOf(item.key) === opt
              ? 'bg-foreground text-background'
              : 'text-muted-foreground hover:bg-muted'"
            @click="setState(item, opt)"
          >
            {{ t(`bots.backup.strategy.${opt}`) }}
          </button>
        </div>

        <!-- Expand details -->
        <button
          v-if="hasDetail(item)"
          type="button"
          class="flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-muted"
          :aria-label="t('bots.backup.toggleDetails')"
          @click.stop="toggleExpand(item.key)"
        >
          <ChevronDown
            class="size-3.5 transition-transform"
            :class="{ 'rotate-180': expanded[item.key] }"
          />
        </button>

        <!-- Include check -->
        <Checkbox
          v-if="mode === 'include' && isAvailable(item)"
          :model-value="stateOf(item.key) !== 'skip'"
          :disabled="disabled"
          :aria-label="t(`bots.backup.sections.${item.key}`)"
          @update:model-value="(checked) => setState(item, checked === true ? 'merge' : 'skip')"
        />
      </div>

      <!-- Detail list -->
      <div
        v-if="expanded[item.key] && hasDetail(item)"
        class="max-h-40 space-y-0.5 overflow-y-auto border-t border-border/40 bg-background/60 px-3 py-2"
      >
        <div
          v-for="(label, i) in item.items"
          :key="i"
          class="truncate font-mono text-[10px] text-muted-foreground"
          :title="label"
        >
          {{ label }}
        </div>
      </div>
    </div>
  </div>
</template>
