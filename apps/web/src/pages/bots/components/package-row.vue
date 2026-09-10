<script setup lang="ts">
// One Package on the bot: icon, name, description, the one primary action its
// state calls for and a menu for the rest. The row itself opens the Package's
// page, where its Skills, dependencies and connectors live. Nothing here
// starts an operation — every choice is emitted and the panel owns
// confirmation and streaming.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ChevronRight,
  ExternalLink,
  MoreHorizontal,
  Package as PackageIcon,
  Trash2,
} from 'lucide-vue-next'
import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  SettingsRow,
  Spinner,
} from '@felinic/ui'
import SkillIcon from '@/pages/supermarket/components/skill-icon.vue'
import {
  packageDisplayDescription,
  packageDisplayName,
  packageInProgress,
  type PackageItem,
} from '@/composables/api/usePackages'
import type { DependencyWorkspaceState } from '@/composables/api/useWorkspaceDependencies'
import { packagePrimaryAction, type PackageRowAction } from './package-actions'

const props = withDefaults(defineProps<{
  item: PackageItem
  workspaceState?: DependencyWorkspaceState
  /** Another operation is streaming for this bot: nothing else may start. */
  busy?: boolean
  /** This client holds the Package's stream. */
  ownsStream?: boolean
}>(), {
  workspaceState: undefined,
  busy: false,
  ownsStream: false,
})

const emit = defineEmits<{
  action: [action: PackageRowAction]
}>()

const { t, locale } = useI18n()

const name = computed(() => packageDisplayName(props.item, locale.value))
const description = computed(() => packageDisplayDescription(props.item, locale.value))
const discovered = computed(() => props.item.status === 'discovered' || !props.item.installation_id)
const inProgress = computed(() => packageInProgress(props.item))
const readonly = computed(() => props.workspaceState !== 'running' && props.workspaceState !== undefined)
const primary = computed(() => packagePrimaryAction(props.item, { busy: props.busy, ownsStream: props.ownsStream, readonly: readonly.value }))
const canRemove = computed(() => !discovered.value && !inProgress.value)
</script>

<template>
  <SettingsRow
    align="start"
    :class="rowClass"
    role="button"
    tabindex="0"
    @click="emit('action', 'open')"
    @keydown.enter.prevent="emit('action', 'open')"
    @keydown.space.prevent="emit('action', 'open')"
  >
    <template #leading>
      <span class="flex size-9 items-center justify-center overflow-hidden rounded-md bg-accent">
        <SkillIcon
          v-if="item.icon"
          :icon="item.icon"
        />
        <PackageIcon
          v-else
          class="size-5 text-muted-foreground"
        />
      </span>
    </template>

    <template #content>
      <div class="min-w-0">
        <div class="flex items-center gap-2">
          <span class="truncate text-control font-medium text-foreground">{{ name }}</span>
          <Spinner v-if="inProgress" />
        </div>
        <p
          v-if="description"
          class="mt-0.5 line-clamp-2 text-body text-muted-foreground"
        >
          {{ description }}
        </p>
        <p
          v-if="item.last_error && (item.status === 'failed' || item.status === 'partial')"
          class="mt-1 break-all font-mono text-caption text-destructive"
        >
          {{ item.last_error }}
        </p>
      </div>
    </template>

    <div
      class="flex items-center gap-2"
      @click.stop
      @keydown.stop
    >
      <Button
        v-if="primary"
        size="sm"
        :variant="primary.variant"
        :disabled="primary.disabled"
        @click="emit('action', primary.action)"
      >
        {{ t(primary.labelKey) }}
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
            {{ t('packages.action.open') }}
          </DropdownMenuItem>
          <template v-if="canRemove">
            <DropdownMenuSeparator />
            <DropdownMenuItem
              variant="destructive"
              :disabled="busy || readonly"
              @select="emit('action', 'remove')"
            >
              <Trash2 />
              {{ t('packages.action.remove') }}
            </DropdownMenuItem>
          </template>
        </DropdownMenuContent>
      </DropdownMenu>

      <ChevronRight
        class="size-4 text-muted-foreground"
        aria-hidden="true"
      />
    </div>
  </SettingsRow>
</template>

<script lang="ts">
// The whole row opens the Package page; hover is the owner-level feedback.
const rowClass = 'cursor-pointer transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset' /* ui-allow-style */
</script>
