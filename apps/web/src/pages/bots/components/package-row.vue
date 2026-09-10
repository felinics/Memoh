<script setup lang="ts">
// One Package on the bot: icon, name, version, status, the one primary action
// its state calls for, a menu for the rest, and a disclosure listing its
// components. Skills are read-only; dependency rows reuse the dependency row
// (update / reinstall / rollback / script); connector rows offer authorization
// and the enabled switch. Nothing here starts an operation — every choice is
// emitted and the panel owns confirmation and streaming.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ChevronRight,
  ExternalLink,
  MoreHorizontal,
  Package as PackageIcon,
  Plug,
  RefreshCw,
  Trash2,
} from 'lucide-vue-next'
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
  Switch,
  TextButton,
} from '@felinic/ui'
import type { ConnectitConnector, ConnectorsConnector } from '@memohai/sdk'
import ProviderIcon from '@/components/provider-icon/index.vue'
import SkillIcon from '@/pages/supermarket/components/skill-icon.vue'
import {
  packageDisplayDescription,
  packageDisplayName,
  packageInProgress,
  packageUpdateAvailable,
  type PackageConnectorItem,
  type PackageDependencyItem,
  type PackageItem,
} from '@/composables/api/usePackages'
import type { DependencyItem, DependencyWorkspaceState } from '@/composables/api/useWorkspaceDependencies'
import type { DependencyMenuAction, DependencyPrimaryAction } from '@/utils/workspace-dependency'
import DependencyRow from './dependency-row.vue'

export type PackageRowAction = 'install' | 'resume' | 'retry' | 'update' | 'viewProgress' | 'remove' | 'open'
export type PackageConnectorAction = 'authorize' | 'reauthorize'

const props = withDefaults(defineProps<{
  item: PackageItem
  workspaceState?: DependencyWorkspaceState
  /** Another operation is streaming for this bot: nothing else may start. */
  busy?: boolean
  /** This client holds the Package's stream. */
  ownsStream?: boolean
  /** Whether this client holds a dependency's stream, by id. */
  dependencyOwnsStream?: (depId: string) => boolean
  /** Connect-It catalog by connector type, for names, icons and auth methods. */
  connectorCatalog?: Map<string, ConnectitConnector>
  connectorsEnabled?: boolean
  /** In-flight connector toggles, keyed by connection id. */
  connectorPending?: Set<string>
}>(), {
  workspaceState: undefined,
  busy: false,
  ownsStream: false,
  dependencyOwnsStream: () => false,
  connectorCatalog: () => new Map(),
  connectorsEnabled: false,
  connectorPending: () => new Set(),
})

const emit = defineEmits<{
  action: [action: PackageRowAction]
  dependencyPrimary: [dependency: DependencyItem, action: DependencyPrimaryAction]
  dependencyMenu: [dependency: DependencyItem, action: DependencyMenuAction]
  connector: [connector: PackageConnectorItem, action: PackageConnectorAction]
  connectorEnabled: [connection: ConnectorsConnector, enabled: boolean]
}>()

const { t, locale } = useI18n()

const name = computed(() => packageDisplayName(props.item, locale.value))
const description = computed(() => packageDisplayDescription(props.item, locale.value))
const discovered = computed(() => props.item.status === 'discovered' || !props.item.installation_id)
const inProgress = computed(() => packageInProgress(props.item))
const updateAvailable = computed(() => packageUpdateAvailable(props.item))
const open = ref(false)

const skills = computed(() => props.item.skills ?? [])
const dependencies = computed<PackageDependencyItem[]>(() => props.item.dependencies ?? [])
const connectors = computed<PackageConnectorItem[]>(() => props.item.connectors ?? [])
const componentCount = computed(() => skills.value.length + dependencies.value.length + connectors.value.length)

const badge = computed<{ variant: 'outline' | 'secondary' | 'destructive' | 'warning' | 'info' | 'success'; key: string; args?: Record<string, string>; spinner?: boolean }>(() => {
  if (inProgress.value) return { variant: 'secondary', key: `packages.status.${props.item.status}`, spinner: true }
  switch (props.item.status) {
    case 'failed':
      return { variant: 'destructive', key: 'packages.status.failed' }
    case 'partial':
      return { variant: 'warning', key: 'packages.status.partial' }
    case 'discovered':
      return { variant: 'outline', key: 'packages.status.discovered' }
    default:
      break
  }
  if (updateAvailable.value) {
    return { variant: 'info', key: 'packages.status.updateAvailable', args: { version: props.item.available_version || props.item.available_revision?.slice(0, 8) || '' } }
  }
  return { variant: 'success', key: 'packages.status.installed' }
})

const readonly = computed(() => props.workspaceState !== 'running' && props.workspaceState !== undefined)

const primary = computed<{ action: PackageRowAction; labelKey: string; variant: 'default' | 'outline'; disabled: boolean } | null>(() => {
  if (inProgress.value) {
    if (!props.ownsStream) return null
    return { action: 'viewProgress', labelKey: 'packages.action.viewProgress', variant: 'outline', disabled: false }
  }
  if (discovered.value) return { action: 'install', labelKey: 'packages.action.install', variant: 'default', disabled: props.busy }
  if (props.item.status === 'failed') return { action: 'retry', labelKey: 'common.retry', variant: 'default', disabled: props.busy || readonly.value }
  if (props.item.status === 'partial') return { action: 'resume', labelKey: 'packages.action.resume', variant: 'default', disabled: props.busy || readonly.value }
  if (updateAvailable.value) return { action: 'update', labelKey: 'packages.action.update', variant: 'default', disabled: props.busy || readonly.value }
  return null
})

const canRemove = computed(() => !discovered.value && !inProgress.value)

function connectorMeta(connector: PackageConnectorItem): ConnectitConnector | undefined {
  return connector.type ? props.connectorCatalog.get(connector.type) : undefined
}

function connectorName(connector: PackageConnectorItem): string {
  return connectorMeta(connector)?.name || connector.type || t('connectors.unknown')
}

function connectorStatusLabel(connector: PackageConnectorItem): string {
  const connection = connector.connector
  if (!connector.connection_id || !connection) return t('packages.connector.needsAuth')
  if (!connection.enabled) return t('connectors.status.disabled')
  switch (connection.status) {
    case 'active': return t('connectors.status.active')
    case 'pending': return t('connectors.status.pending')
    case 'reauth_required': return t('connectors.status.reauthRequired')
    case 'authorization_failed': return t('connectors.status.authorizationFailed')
    default: return t('connectors.status.unavailable')
  }
}

function connectorNeedsReauth(connector: PackageConnectorItem): boolean {
  const status = connector.connector?.status
  return !!connector.connection_id && (status === 'pending' || status === 'reauth_required' || status === 'authorization_failed')
}

function dependencyName(dep: PackageDependencyItem): string {
  return dep.dependency?.name || dep.id || ''
}
</script>

<template>
  <SettingsRow align="start">
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
        <div class="flex flex-wrap items-center gap-2">
          <span class="truncate text-control font-medium text-foreground">{{ name }}</span>
          <Badge
            v-if="item.version"
            variant="secondary"
            size="sm"
            font="mono"
          >
            {{ item.version }}
          </Badge>
          <Badge
            :variant="badge.variant"
            size="sm"
          >
            <Spinner v-if="badge.spinner" />
            {{ t(badge.key, badge.args ?? {}) }}
          </Badge>
          <Badge
            v-if="item.reason === 'required'"
            variant="outline"
            size="sm"
          >
            {{ t('packages.reason.required') }}
          </Badge>
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

        <Collapsible
          v-if="componentCount"
          v-model:open="open"
          class="mt-2"
        >
          <CollapsibleTrigger as-child>
            <TextButton class="-ml-1.5">
              <ChevronRight
                class="transition-transform"
                :class="{ 'rotate-90': open }"
              />
              {{ t('packages.components', { count: componentCount }, componentCount) }}
            </TextButton>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div class="mt-2 space-y-4 rounded-lg border border-border bg-muted-soft/40 p-3">
              <section v-if="skills.length">
                <h4 class="mb-1 text-caption font-medium uppercase tracking-wide text-muted-foreground">
                  {{ t('packages.section.skills', { count: skills.length }, skills.length) }}
                </h4>
                <ul class="space-y-1">
                  <li
                    v-for="skill in skills"
                    :key="skill.skill_id"
                    class="flex min-w-0 items-start gap-2 text-body"
                  >
                    <span class="mt-0.5 flex size-5 shrink-0 items-center justify-center">
                      <SkillIcon :icon="skill.icon" />
                    </span>
                    <span class="min-w-0">
                      <span class="font-medium">{{ skill.name || skill.skill_id }}</span>
                      <span
                        v-if="skill.description"
                        class="ml-1 text-muted-foreground"
                      >{{ skill.description }}</span>
                    </span>
                  </li>
                </ul>
              </section>

              <section v-if="dependencies.length">
                <h4 class="mb-1 text-caption font-medium uppercase tracking-wide text-muted-foreground">
                  {{ t('packages.section.dependencies', { count: dependencies.length }, dependencies.length) }}
                </h4>
                <div class="divide-y divide-border rounded-md border border-border bg-background">
                  <template
                    v-for="dep in dependencies"
                    :key="dep.id"
                  >
                    <DependencyRow
                      v-if="dep.dependency"
                      :item="dep.dependency"
                      :workspace-state="workspaceState"
                      :busy="busy"
                      :owns-stream="dependencyOwnsStream(dep.id ?? '')"
                      :shared="dep.shared"
                      @primary="emit('dependencyPrimary', dep.dependency, $event)"
                      @menu="emit('dependencyMenu', dep.dependency, $event)"
                    />
                    <div
                      v-else
                      class="flex items-center gap-2 px-3 py-2 text-body text-muted-foreground"
                    >
                      <PackageIcon class="size-4" />
                      {{ dependencyName(dep) }}
                      <Badge
                        variant="outline"
                        size="sm"
                      >
                        {{ t('packages.dependency.unknown') }}
                      </Badge>
                    </div>
                  </template>
                </div>
              </section>

              <section v-if="connectors.length">
                <h4 class="mb-1 text-caption font-medium uppercase tracking-wide text-muted-foreground">
                  {{ t('packages.section.connectors', { count: connectors.length }, connectors.length) }}
                </h4>
                <div class="divide-y divide-border rounded-md border border-border bg-background">
                  <div
                    v-for="connector in connectors"
                    :key="connector.type"
                    class="flex min-w-0 items-center gap-3 px-3 py-2"
                  >
                    <ProviderIcon
                      :icon="connectorMeta(connector)?.icon_url || ''"
                      class="size-5 object-contain"
                    >
                      <Plug class="size-4 text-muted-foreground" />
                    </ProviderIcon>
                    <div class="min-w-0 flex-1">
                      <div class="flex flex-wrap items-center gap-2">
                        <span class="truncate text-body font-medium">{{ connectorName(connector) }}</span>
                        <Badge
                          v-if="connector.required === false"
                          variant="outline"
                          size="sm"
                        >
                          {{ t('packages.connector.optional') }}
                        </Badge>
                        <Badge
                          variant="outline"
                          size="sm"
                        >
                          {{ t('packages.connector.shared') }}
                        </Badge>
                      </div>
                      <p class="text-caption text-muted-foreground">
                        {{ connectorStatusLabel(connector) }}
                      </p>
                    </div>
                    <template v-if="!discovered">
                      <Button
                        v-if="!connector.connection_id"
                        size="sm"
                        variant="outline"
                        :disabled="!connectorsEnabled || busy"
                        @click="emit('connector', connector, 'authorize')"
                      >
                        {{ t('packages.connector.authorize') }}
                      </Button>
                      <Button
                        v-else-if="connectorNeedsReauth(connector)"
                        size="sm"
                        variant="outline"
                        :disabled="!connectorsEnabled"
                        @click="emit('connector', connector, 'reauthorize')"
                      >
                        {{ t('connectors.reauthorize') }}
                      </Button>
                      <Switch
                        v-if="connector.connector"
                        :model-value="connector.connector.enabled"
                        :disabled="connectorPending.has(connector.connection_id ?? '')"
                        :aria-label="t('connectors.enabled')"
                        @update:model-value="emit('connectorEnabled', connector.connector, $event)"
                      />
                    </template>
                  </div>
                </div>
              </section>
            </div>
          </CollapsibleContent>
        </Collapsible>
      </div>
    </template>

    <div class="flex items-center gap-2">
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
          <DropdownMenuItem @select="emit('action', 'open')">
            <ExternalLink />
            {{ t('packages.action.open') }}
          </DropdownMenuItem>
          <DropdownMenuItem
            v-if="!discovered && !inProgress && !updateAvailable"
            :disabled="busy || readonly"
            @select="emit('action', 'update')"
          >
            <RefreshCw />
            {{ t('packages.action.update') }}
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
    </div>
  </SettingsRow>
</template>
