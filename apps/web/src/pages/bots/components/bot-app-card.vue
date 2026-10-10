<script setup lang="ts">
// One App on the bot as a Supermarket-style card: icon, name,
// description, the one primary action its state calls for and a menu for the
// rest. The card itself opens the App's page. Nothing here starts an
// operation — every choice is emitted and the panel owns confirmation and
// streaming.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  AlertTriangle,
  ExternalLink,
  MoreHorizontal,
  Package as AppIcon,
  Trash2,
} from 'lucide-vue-next'
import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  Spinner,
} from '@felinic/ui'
import MarketItemCard from '@/pages/supermarket/components/market-item-card.vue'
import SkillIcon from '@/pages/supermarket/components/skill-icon.vue'
import {
  appDisplayDescription,
  appDisplayName,
  appInProgress,
  type AppItem,
} from '@/composables/api/useApps'
import type { DependencyWorkspaceState } from '@/composables/api/useWorkspaceDependencies'
import { useWorkspaceDependencyText } from '@/composables/useWorkspaceDependencyText'
import { appCanTry, appPrimaryAction, type AppRowAction } from './app-actions'

const props = withDefaults(defineProps<{
  item: AppItem
  workspaceState?: DependencyWorkspaceState
  /** Another operation is streaming for this bot: nothing else may start. */
  busy?: boolean
  /** This client holds the App's stream. */
  ownsStream?: boolean
}>(), {
  workspaceState: undefined,
  busy: false,
  ownsStream: false,
})

const emit = defineEmits<{
  action: [action: AppRowAction]
}>()

const { t, locale } = useI18n()
const { appDependencyIconUrl } = useWorkspaceDependencyText()

const fallbackIconUrl = computed(() => appDependencyIconUrl(props.item))

const name = computed(() => appDisplayName(props.item, locale.value))
const description = computed(() => appDisplayDescription(props.item, locale.value))
const discovered = computed(() => props.item.status === 'discovered' || !props.item.installation_id)
const inProgress = computed(() => appInProgress(props.item))
const readonly = computed(() => props.workspaceState !== 'running' && props.workspaceState !== undefined)
const primary = computed(() => appPrimaryAction(props.item, { busy: props.busy, ownsStream: props.ownsStream, readonly: readonly.value }))
const canTry = computed(() => appCanTry(props.item, { readonly: readonly.value }))
const canRemove = computed(() => !discovered.value && !inProgress.value)
const needsAttention = computed(() => props.item.status === 'failed' || props.item.status === 'partial')
</script>

<template>
  <MarketItemCard
    :name="name"
    :description="description"
    :homepage="item.homepage"
    @open="emit('action', 'open')"
  >
    <template #leading>
      <SkillIcon
        v-if="item.icon"
        :icon="item.icon"
      />
      <img
        v-else-if="fallbackIconUrl"
        :src="fallbackIconUrl"
        alt=""
        class="size-5 object-contain"
      >
      <AppIcon
        v-else
        class="size-5 text-muted-foreground"
      />
    </template>

    <template
      v-if="needsAttention"
      #meta
    >
      <span
        class="flex items-center gap-1 text-caption"
        :class="item.status === 'failed' ? 'text-destructive' : 'text-warning-foreground'"
      >
        <AlertTriangle
          class="size-3 shrink-0"
          aria-hidden="true"
        />
        {{ t(item.status === 'failed' ? 'apps.diagnostics.failed' : 'apps.diagnostics.partial') }}
      </span>
    </template>

    <template #actions>
      <div class="flex items-center gap-1">
        <Spinner v-if="inProgress" />
        <Button
          v-if="canTry"
          size="sm"
          variant="outline"
          @click="emit('action', 'tryIt')"
        >
          {{ t('apps.action.tryIt') }}
        </Button>
        <Button
          v-if="primary"
          size="sm"
          :variant="primary.variant"
          :disabled="primary.disabled"
          @click="emit('action', primary.action)"
        >
          {{ t(primary.labelKey) }}
        </Button>
        <Button
          v-if="item.status === 'failed' && canRemove"
          size="sm"
          variant="destructive"
          :disabled="busy || readonly"
          @click="emit('action', 'remove')"
        >
          {{ t('apps.action.remove') }}
        </Button>
        <DropdownMenu>
          <DropdownMenuTrigger as-child>
            <Button
              variant="ghost"
              size="icon-sm"
              :aria-label="t('common.actions')"
            >
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem @select="emit('action', 'openSupermarket')">
              <ExternalLink />
              {{ t('apps.action.open') }}
            </DropdownMenuItem>
            <template v-if="canRemove && item.status !== 'failed'">
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                :disabled="busy || readonly"
                @select="emit('action', 'remove')"
              >
                <Trash2 />
                {{ t('apps.action.remove') }}
              </DropdownMenuItem>
            </template>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </template>
  </MarketItemCard>
</template>
