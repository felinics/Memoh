<script setup lang="ts">
// One dependency in the panel: brand mark, name + version + status, the one
// primary action the state calls for, and a menu for the rest. Every rule
// that decides what shows lives in utils/workspace-dependency (unit-tested);
// this file only lays the answers out. The row never starts anything itself —
// it emits the chosen action and the panel owns confirmation and streaming.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight, FileCode, MoreHorizontal, RotateCw, Trash2, Undo2 } from 'lucide-vue-next'
import {
  Badge,
  Button,
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  SettingsRow,
  Spinner,
  TextButton,
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@felinic/ui'
import type { DependencyItem, DependencyWorkspaceState } from '@/composables/api/useWorkspaceDependencies'
import {
  dependencyDisplayName,
  dependencyIcon,
  dependencyMenuActions,
  dependencyPlatformUnsupported,
  dependencyPrimaryAction,
  dependencyStatusBadge,
  formatDependencyVersion,
  type DependencyMenuAction,
  type DependencyMenuActionKind,
  type DependencyPrimaryAction,
} from '@/utils/workspace-dependency'

const props = withDefaults(defineProps<{
  item: DependencyItem
  workspaceState?: DependencyWorkspaceState
  /** Another dependency's operation is streaming: nothing else may start. */
  busy?: boolean
  /** This client holds the row's stream, so it can show the log. */
  ownsStream?: boolean
}>(), {
  workspaceState: undefined,
  busy: false,
  ownsStream: false,
})

const emit = defineEmits<{
  primary: [action: DependencyPrimaryAction]
  menu: [action: DependencyMenuAction]
}>()

const { t } = useI18n()

const name = computed(() => dependencyDisplayName(props.item))
const version = computed(() => formatDependencyVersion(props.item.installed_version))
const icon = computed(() => dependencyIcon(props.item))
const badge = computed(() => dependencyStatusBadge(props.item))
const unsupported = computed(() => dependencyPlatformUnsupported(props.item))
const failed = computed(() => props.item.status === 'failed')
const lastError = computed(() => (props.item.last_error ?? '').trim())
const errorOpen = ref(false)

// Image-provided rows say so instead of repeating the catalog blurb: the one
// thing a user wants to know about them is why "Remove" is greyed out.
const description = computed(() => (
  props.item.source === 'image'
    ? t('bots.dependencies.imageProvidedDescription')
    : (props.item.description ?? '').trim()
))

const primary = computed(() => dependencyPrimaryAction(props.item, props.workspaceState, { ownsStream: props.ownsStream }))
const menu = computed(() => dependencyMenuActions(props.item, props.workspaceState))

// Only the running row may be inspected while something streams; every other
// start button waits so two scripts never race inside one workspace.
const primaryDisabled = computed(() => !!primary.value && (primary.value.disabled || (props.busy && primary.value.kind !== 'viewProgress')))
function menuDisabled(action: DependencyMenuAction): boolean {
  return action.disabled || (props.busy && action.kind !== 'viewScript')
}

function menuIcon(kind: DependencyMenuActionKind) {
  switch (kind) {
    case 'reinstall':
      return RotateCw
    case 'rollback':
      return Undo2
    case 'remove':
      return Trash2
    default:
      return FileCode
  }
}

// An unsupported row is dimmed as a whole (no pointer-events-none: the badge
// tooltip must still open) so it reads as "not for this workspace", not broken.
const dimClass = computed(() => (unsupported.value ? 'opacity-40' : ''))
</script>

<template>
  <SettingsRow :align="failed && lastError ? 'start' : 'center'">
    <template #leading>
      <span
        class="flex size-9 items-center justify-center"
        :class="dimClass"
      >
        <component
          :is="icon"
          class="size-5"
        />
      </span>
    </template>

    <template #content>
      <div :class="dimClass">
        <div class="flex flex-wrap items-center gap-2">
          <span class="truncate text-control font-medium text-foreground">{{ name }}</span>
          <Badge
            v-if="version"
            variant="secondary"
            size="sm"
            font="mono"
          >
            {{ version }}
          </Badge>

          <TooltipProvider v-if="badge.tooltipKey">
            <Tooltip :delay-duration="300">
              <TooltipTrigger as-child>
                <Badge
                  :variant="badge.variant"
                  size="sm"
                  tabindex="0"
                >
                  {{ t(badge.key, badge.args ?? {}) }}
                </Badge>
              </TooltipTrigger>
              <TooltipContent side="bottom">
                {{ t(badge.tooltipKey) }}
              </TooltipContent>
            </Tooltip>
          </TooltipProvider>
          <Badge
            v-else
            :variant="badge.variant"
            size="sm"
          >
            <Spinner v-if="badge.spinner" />
            {{ t(badge.key, badge.args ?? {}) }}
          </Badge>

          <Badge
            v-if="item.source === 'image'"
            variant="outline"
            size="sm"
          >
            {{ t('bots.dependencies.source.image') }}
          </Badge>
        </div>

        <p
          v-if="description"
          class="mt-0.5 text-body text-muted-foreground"
        >
          {{ description }}
        </p>

        <!-- The recorded failure is a disclosure, not always-on text: a stack
             trace would otherwise set the row height for the whole list. -->
        <Collapsible
          v-if="failed && lastError"
          v-model:open="errorOpen"
          class="mt-1"
        >
          <CollapsibleTrigger as-child>
            <TextButton class="-ml-1.5">
              <ChevronRight
                class="transition-transform"
                :class="{ 'rotate-90': errorOpen }"
              />
              {{ t('bots.dependencies.errorDetails') }}
            </TextButton>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <pre class="mt-1 max-h-40 overflow-y-auto whitespace-pre-wrap break-all font-mono text-caption text-destructive">{{ lastError }}</pre>
          </CollapsibleContent>
        </Collapsible>
      </div>
    </template>

    <div
      v-if="primary || menu.length"
      class="flex items-center gap-2"
    >
      <Button
        v-if="primary"
        size="sm"
        :variant="primary.variant"
        :disabled="primaryDisabled"
        @click="emit('primary', primary)"
      >
        {{ t(primary.labelKey) }}
      </Button>

      <DropdownMenu v-if="menu.length">
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
          <template
            v-for="action in menu"
            :key="action.kind"
          >
            <DropdownMenuSeparator v-if="action.separatorBefore" />
            <DropdownMenuItem
              :variant="action.destructive ? 'destructive' : 'default'"
              :disabled="menuDisabled(action)"
              @select="emit('menu', action)"
            >
              <component :is="menuIcon(action.kind)" />
              {{ t(action.labelKey, action.args ?? {}) }}
            </DropdownMenuItem>
          </template>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  </SettingsRow>
</template>
