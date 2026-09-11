<script setup lang="ts">
// The page of one App on the bot, laid out like the Supermarket detail
// page: the icon box and title with the actions beside them, the
// description, then its components. The back row belongs to DetailPane. Skills are read-only; dependency rows reuse
// the dependency row (update / reinstall / rollback / script); connector rows
// offer authorization and the enabled switch. Nothing here starts an
// operation — every choice is emitted and the panel owns confirmation and
// streaming.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ExternalLink,
  MoreHorizontal,
  Package as AppIcon,
  Plug,
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
  SettingsSection,
  Spinner,
  Switch,
} from '@felinic/ui'
import type { ConnectitConnector, ConnectorsConnector } from '@memohai/sdk'
import ProviderIcon from '@/components/provider-icon/index.vue'
import SkillIcon from '@/pages/supermarket/components/skill-icon.vue'
import {
  appDisplayDescription,
  appDisplayName,
  appInProgress,
  type AppConnectorItem,
  type AppDependencyItem,
  type AppItem,
} from '@/composables/api/useApps'
import type { DependencyItem, DependencyWorkspaceState } from '@/composables/api/useWorkspaceDependencies'
import type { DependencyMenuAction, DependencyPrimaryAction } from '@/utils/workspace-dependency'
import { useWorkspaceDependencyText } from '@/composables/useWorkspaceDependencyText'
import DependencyRow from './dependency-row.vue'
import { appPrimaryAction, type AppRowAction } from './app-actions'

export type AppConnectorAction = 'authorize' | 'reauthorize'

const props = withDefaults(defineProps<{
  item: AppItem
  workspaceState?: DependencyWorkspaceState
  /** Another operation is streaming for this bot: nothing else may start. */
  busy?: boolean
  /** This client holds the App's stream. */
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
  action: [action: AppRowAction]
  dependencyPrimary: [dependency: DependencyItem, action: DependencyPrimaryAction]
  dependencyMenu: [dependency: DependencyItem, action: DependencyMenuAction]
  connector: [connector: AppConnectorItem, action: AppConnectorAction]
  connectorEnabled: [connection: ConnectorsConnector, enabled: boolean]
}>()

const { t, locale } = useI18n()
const { dependencyIconUrl } = useWorkspaceDependencyText()

// A discovered App has no release, so no icon of its own; its dependency
// carries the same artwork.
const fallbackIconUrl = computed(() => {
  if (props.item.icon) return ''
  const deps = props.item.dependencies ?? []
  const dep = deps.find(entry => entry.id === props.item.app_id) ?? deps[0]
  return dep?.dependency ? dependencyIconUrl(dep.dependency) : ''
})

// Same icon box as the Supermarket detail header.
const iconBoxClass = 'flex size-16 shrink-0 items-center justify-center overflow-hidden rounded-md border bg-background shadow-sm' /* ui-allow-style */

const name = computed(() => appDisplayName(props.item, locale.value))
const description = computed(() => appDisplayDescription(props.item, locale.value))
const discovered = computed(() => props.item.status === 'discovered' || !props.item.installation_id)
const inProgress = computed(() => appInProgress(props.item))
const readonly = computed(() => props.workspaceState !== 'running' && props.workspaceState !== undefined)
const primary = computed(() => appPrimaryAction(props.item, { busy: props.busy, ownsStream: props.ownsStream, readonly: readonly.value }))
const canRemove = computed(() => !discovered.value && !inProgress.value)

const skills = computed(() => props.item.skills ?? [])
const dependencies = computed<AppDependencyItem[]>(() => props.item.dependencies ?? [])
const connectors = computed<AppConnectorItem[]>(() => props.item.connectors ?? [])

function connectorMeta(connector: AppConnectorItem): ConnectitConnector | undefined {
  return connector.type ? props.connectorCatalog.get(connector.type) : undefined
}

function connectorName(connector: AppConnectorItem): string {
  return connectorMeta(connector)?.name || connector.type || t('connectors.unknown')
}

function connectorStatusLabel(connector: AppConnectorItem): string {
  const connection = connector.connector
  if (!connector.connection_id || !connection) return t('apps.connector.needsAuth')
  if (!connection.enabled) return t('connectors.status.disabled')
  switch (connection.status) {
    case 'active': return t('connectors.status.active')
    case 'pending': return t('connectors.status.pending')
    case 'reauth_required': return t('connectors.status.reauthRequired')
    case 'authorization_failed': return t('connectors.status.authorizationFailed')
    default: return t('connectors.status.unavailable')
  }
}

function connectorNeedsReauth(connector: AppConnectorItem): boolean {
  const status = connector.connector?.status
  return !!connector.connection_id && (status === 'pending' || status === 'reauth_required' || status === 'authorization_failed')
}

function dependencyName(dep: AppDependencyItem): string {
  return dep.dependency?.name || dep.id || ''
}
</script>

<template>
  <div>
    <header class="flex items-start gap-4">
      <div :class="iconBoxClass">
        <SkillIcon
          v-if="item.icon"
          :icon="item.icon"
          variant="detail"
        />
        <img
          v-else-if="fallbackIconUrl"
          :src="fallbackIconUrl"
          alt=""
          class="size-8 object-contain"
        >
        <AppIcon
          v-else
          class="size-8 text-muted-foreground"
        />
      </div>
      <div class="min-w-0 flex-1">
        <h1 class="break-words text-3xl font-semibold leading-tight">
          {{ name }}
        </h1>
      </div>
      <div class="flex shrink-0 items-center gap-2">
        <Spinner v-if="inProgress" />
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
              {{ t('apps.action.open') }}
            </DropdownMenuItem>
            <template v-if="canRemove">
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
    </header>

    <p class="mt-8 max-w-4xl text-base leading-7 text-muted-foreground">
      {{ description || t('supermarket.noDescription') }}
    </p>
    <p
      v-if="item.last_error && (item.status === 'failed' || item.status === 'partial')"
      class="mt-2 break-all font-mono text-caption text-destructive"
    >
      {{ item.last_error }}
    </p>

    <section
      v-if="skills.length"
      class="mt-8 space-y-3"
    >
      <h3 class="text-base font-semibold">
        {{ t('apps.sections.skills') }}
        <span class="ml-1.5 font-normal text-muted-foreground">{{ skills.length }}</span>
      </h3>
      <SettingsSection>
        <SettingsRow
          v-for="skill in skills"
          :key="skill.skill_id"
          align="start"
        >
          <template #leading>
            <span class="flex size-9 items-center justify-center overflow-hidden rounded-md bg-accent">
              <SkillIcon :icon="skill.icon" />
            </span>
          </template>
          <template #content>
            <p class="text-control font-medium text-foreground">
              {{ skill.name || skill.skill_id }}
            </p>
            <p
              v-if="skill.description"
              class="mt-0.5 text-body text-muted-foreground"
            >
              {{ skill.description }}
            </p>
          </template>
        </SettingsRow>
      </SettingsSection>
    </section>

    <section
      v-if="dependencies.length"
      class="mt-8 space-y-3"
    >
      <h3 class="text-base font-semibold">
        {{ t('apps.sections.dependencies') }}
        <span class="ml-1.5 font-normal text-muted-foreground">{{ dependencies.length }}</span>
      </h3>
      <div class="divide-y divide-border rounded-lg border border-border bg-background">
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
            <AppIcon class="size-4" />
            {{ dependencyName(dep) }}
            <span class="text-caption">{{ t('apps.dependency.unknown') }}</span>
          </div>
        </template>
      </div>
    </section>

    <section
      v-if="connectors.length"
      class="mt-8 space-y-3"
    >
      <h3 class="text-base font-semibold">
        {{ t('apps.sections.connectors') }}
        <span class="ml-1.5 font-normal text-muted-foreground">{{ connectors.length }}</span>
      </h3>
      <SettingsSection>
        <SettingsRow
          v-for="connector in connectors"
          :key="connector.type"
        >
          <template #leading>
            <span class="flex size-9 items-center justify-center overflow-hidden rounded-md bg-accent">
              <ProviderIcon
                :icon="connectorMeta(connector)?.icon_url || ''"
                class="size-5 object-contain"
              >
                <Plug class="size-4 text-muted-foreground" />
              </ProviderIcon>
            </span>
          </template>
          <template #content>
            <p class="text-control font-medium text-foreground">
              {{ connectorName(connector) }}
            </p>
            <p class="mt-0.5 text-body text-muted-foreground">
              {{ connectorStatusLabel(connector) }}
              <template v-if="connector.required === false">
                · {{ t('apps.connector.optional') }}
              </template>
            </p>
          </template>
          <div
            v-if="!discovered"
            class="flex items-center gap-2"
          >
            <Button
              v-if="!connector.connection_id"
              size="sm"
              variant="outline"
              :disabled="!connectorsEnabled || busy"
              @click="emit('connector', connector, 'authorize')"
            >
              {{ t('apps.connector.authorize') }}
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
          </div>
        </SettingsRow>
      </SettingsSection>
    </section>
  </div>
</template>
