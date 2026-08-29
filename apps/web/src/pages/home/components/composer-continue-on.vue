<template>
  <DropdownMenu @update:open="onMenuOpen">
    <DropdownMenuTrigger as-child>
      <!-- Icon-only for the default destination (the native cloud workspace):
           a quiet peer of the ＋ button. On md+, the moment the session is
           pinned to a real machine — or holds any non-default selection — the
           trigger grows into the labeled pill: a non-default target is worth
           reading at a glance. On mobile it NEVER expands: the pill and the
           model trigger would squeeze each other into uselessness on a narrow
           row, so the collapsed circle carries it and the selection is read
           in the menu instead.
           The two forms are ONE element morphing, never two nodes swapping:
           a single Laptop glyph, a collapsing label slot (max-width/opacity),
           and a padding transition converge the circle to exactly 32×32
           (44×44 on mobile). Splitting the forms across v-if/v-else nodes
           reads as two different controls mid-switch. The <button> itself
           still never transforms (reka anchors the open menu to its rendered
           rect); press squish lives on .composer-pill-content in BOTH forms
           (composer-pill-press / composer-circle-press, style.css) so press
           feedback is identical either way. -->
      <Button
        type="button"
        variant="ghost"
        size="sm"
        shape="circle"
        :disabled="locked"
        :title="isDefaultTarget ? currentName : t('chat.continueOn.label')"
        :aria-label="t('chat.continueOn.label')"
        class="order-2 min-w-0 max-w-48 self-end max-md:h-11 duration-200 ease-[cubic-bezier(0.16,1,0.3,1)] motion-reduce:transition-none"
        :class="rendersAsPill
          ? 'composer-pill-press shrink'
          : 'composer-circle-press px-2 max-md:px-3'"
      >
        <span class="composer-pill-content inline-flex min-w-0 items-center">
          <Laptop
            class="size-4 max-md:size-5 shrink-0 text-muted-foreground"
            :stroke-width="1.5"
          />
          <!-- Spacing lives on the slot's children (ml-2), not the slot itself:
               a gap/padding on the collapsing container would survive the
               collapse and the circle could never converge to 32px. -->
          <span
            class="inline-flex min-w-0 items-center overflow-hidden transition-[max-width,opacity] duration-200 ease-[cubic-bezier(0.16,1,0.3,1)] motion-reduce:transition-none"
            :class="rendersAsPill ? 'max-w-38 opacity-100' : 'max-w-0 opacity-0'"
            :aria-hidden="!rendersAsPill"
          >
            <span class="ml-2 min-w-0 truncate text-label text-composer-control-label">{{ currentName }}</span>
            <ChevronDown
              class="ml-2 size-3.5 shrink-0 text-muted-foreground"
              :stroke-width="1.5"
            />
          </span>
        </span>
      </Button>
    </DropdownMenuTrigger>
    <!-- Width is MEASURED, not pinned: the panel sizes to its longest row
         (browser max-content), bounded by a floor (short lists don't go
         skinny) and a ceiling. The ceiling is the SMALLER of 20rem and reka's
         measured available width, so narrow phone viewports tighten the cap
         instead of overflowing; very long names still truncate. Note: the
         on-open refetch can widen it mid-open if a newly arrived computer has
         a longer name — the floor keeps that jump one-directional. -->
    <DropdownMenuContent
      class="w-auto min-w-64 max-w-[min(20rem,var(--reka-dropdown-menu-content-available-width))]"
      align="start"
      side="top"
    >
      <DropdownMenuLabel>{{ t('chat.continueOn.label') }}</DropdownMenuLabel>
      <DropdownMenuItem
        v-if="initialLoading"
        disabled
      >
        <Spinner />
        <span class="min-w-0 flex-1 truncate">{{ t('chat.computerLoading') }}</span>
      </DropdownMenuItem>
      <DropdownMenuItem
        v-else-if="loadFailed"
        disabled
      >
        <span class="min-w-0 flex-1 truncate">{{ t('chat.computerLoadFailed') }}</span>
      </DropdownMenuItem>
      <template v-else>
        <!-- A selection whose target vanished (unmounted/revoked) stays
             visible as a disabled ghost so the user can see what the session
             was pinned to. -->
        <DropdownMenuItem
          v-if="selectedMissing"
          disabled
        >
          <Laptop class="size-4 shrink-0" />
          <span class="min-w-0 flex-1 truncate">
            {{ selectedSnapshotName || t('chat.computerUnavailable') }}
          </span>
          <span class="shrink-0 text-caption text-muted-foreground">{{ t('chat.computerUnavailable') }}</span>
          <Check class="ml-auto" />
        </DropdownMenuItem>
        <DropdownMenuItem
          v-for="target in targets"
          :key="target.target_id"
          :disabled="locked || !workspaceTargetAvailable(target)"
          @select="emit('select', target)"
        >
          <component
            :is="target.kind === 'native' ? Cloud : Laptop"
            class="size-4 shrink-0"
          />
          <span class="min-w-0 flex-1 truncate">{{ displayName(target) }}</span>
          <!-- Positive states (default / online) say nothing — being listed at
               all already means usable. Only an unavailable target carries a
               reason label, and the row greys out via :disabled above. -->
          <span
            v-if="!workspaceTargetAvailable(target)"
            class="shrink-0 text-caption text-muted-foreground"
          >
            {{ workspaceTargetStatusLabel(target, t) }}
          </span>
          <Check
            v-if="selectedTargetId === target.target_id"
            class="ml-auto"
          />
        </DropdownMenuItem>

        <!-- With at least one computer in play, management is one click from
             the selector — it should not require knowing the settings page. -->
        <template v-if="hasRemoteTargets">
          <DropdownMenuSeparator />
          <DropdownMenuItem @select="goToRuntimes">
            <Settings class="size-4 shrink-0" />
            <span class="min-w-0 flex-1 truncate">{{ t('chat.continueOn.manageComputers') }}</span>
          </DropdownMenuItem>
        </template>

        <!-- No authorized remote computer: the CTA depends on whether the
             account has computers at all. -->
        <template v-if="!hasRemoteTargets">
          <DropdownMenuSeparator />
          <DropdownMenuItem
            disabled
            class="text-muted-foreground"
          >
            <span class="min-w-0 flex-1 truncate">
              {{ accountRuntimesEmpty ? t('computerAccess.emptyComputers') : t('chat.continueOn.noAccess') }}
            </span>
          </DropdownMenuItem>
          <DropdownMenuItem @select="accountRuntimesEmpty ? goToRuntimes() : (accessDialogOpen = true)">
            <Plus
              v-if="accountRuntimesEmpty"
              class="size-4 shrink-0"
            />
            <Settings
              v-else
              class="size-4 shrink-0"
            />
            <span class="min-w-0 flex-1 truncate">
              {{ accountRuntimesEmpty ? t('chat.continueOn.addComputer') : t('chat.continueOn.manageAccess') }}
            </span>
          </DropdownMenuItem>
        </template>
      </template>
    </DropdownMenuContent>
  </DropdownMenu>

  <BotComputerAccessDialog
    v-if="accessDialogOpen"
    v-model:open="accessDialogOpen"
    :bot="{ id: botId, name: botName }"
  />
</template>

<script setup lang="ts">
import { computed, inject, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import type { WorkspaceWorkspaceTarget } from '@memohai/sdk'
import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  Spinner,
} from '@felinic/ui'
import { Check, ChevronDown, Cloud, Laptop, Plus, Settings } from 'lucide-vue-next'
import {
  DesktopRuntimeKey,
  type DesktopRuntimeState,
} from '@/lib/desktop-shell'
import { useIsMobile } from '@/composables/useIsMobile'
import {
  workspaceTargetAvailable,
  workspaceTargetName,
  workspaceTargetStatusLabel,
} from '@/utils/workspace-target'
import BotComputerAccessDialog from '@/components/computer/bot-computer-access-dialog.vue'
import { useAccountRuntimes } from '@/components/computer/use-computer-access'

// The composer's destination selector ("Continue on"): which authorized
// computer this session runs on. It sits in the controls row as a peer of the
// ＋ menu. Selection only — ACL lives on the account Computers page / bot
// Computer page / access dialog, never here.
const props = defineProps<{
  targets: (WorkspaceWorkspaceTarget & { target_id: string, kind: string })[]
  selectedTargetId: string
  selectedMissing: boolean
  selectedSnapshotName: string
  locked: boolean
  initialLoading: boolean
  loadFailed: boolean
  botId: string
  botName: string
}>()

const emit = defineEmits<{
  select: [target: WorkspaceWorkspaceTarget & { target_id: string, kind: string }]
  menuOpen: []
}>()

const { t } = useI18n()
const router = useRouter()
const desktopRuntimeBridge = inject(DesktopRuntimeKey, undefined)
const desktopRuntimeState = ref<DesktopRuntimeState>()

const { runtimes, refetch: refetchRuntimes } = useAccountRuntimes()
const accessDialogOpen = ref(false)

// Opening the menu is the user's decision moment — refetch so a computer
// connected or authorized elsewhere just now shows up immediately.
function onMenuOpen(open: boolean): void {
  if (!open) return
  void refetchRuntimes()
  emit('menuOpen')
}

const hasRemoteTargets = computed(() => props.targets.some(target => target.kind === 'remote'))
const accountRuntimesEmpty = computed(() => (runtimes.value ?? []).length === 0)

const selectedTarget = computed(() => (
  props.targets.find(target => target.target_id === props.selectedTargetId) ?? null
))

// On desktop, the runtime backed by this machine reads as "This computer"
// instead of its registered name — same rule as the Computers page.
function displayName(target: WorkspaceWorkspaceTarget): string {
  const localId = desktopRuntimeState.value?.runtimeId
  if (desktopRuntimeState.value?.enabled && localId && target.runtime_id === localId) {
    return t('runtimes.thisComputer.title')
  }
  return workspaceTargetName(target, t)
}

const currentName = computed(() => {
  if (selectedTarget.value) return displayName(selectedTarget.value)
  if (props.selectedMissing) return props.selectedSnapshotName || t('chat.computerUnavailable')
  // A selection whose targets haven't loaded yet still wears its snapshot
  // name — the pill is announcing THAT computer, not the generic label.
  return props.selectedSnapshotName || t('chat.continueOn.label')
})

// Only an explicit non-default selection earns the pill. No selection at all —
// including the window before the targets query lands — renders the collapsed
// default circle: otherwise every fresh welcome page flashes a "Continue on"
// pill that collapses the moment the default target resolves.
const isDefaultTarget = computed(() => (
  selectedTarget.value
    ? selectedTarget.value.kind === 'native'
    : !props.selectedTargetId
))

// The pill form only exists on md+; on mobile the trigger always renders the
// circle (see the header comment), so it must also PRESS and read like the
// circle — same composer-circle-press, collapsed slot, hidden from SRs. This
// is the single source of truth for which form is on screen.
const isMobileShell = useIsMobile()
const rendersAsPill = computed(() => !isDefaultTarget.value && !isMobileShell.value)

function goToRuntimes(): void {
  void router.push({ name: 'runtimes' })
}

onMounted(async () => {
  if (!desktopRuntimeBridge) return
  try {
    desktopRuntimeState.value = await desktopRuntimeBridge.runtimeState()
  } catch {
    // The Computers page owns recovery UI for Desktop connection-state errors.
  }
})
</script>
