<template>
  <PageShell
    variant="tab"
    :title="$t('bots.settings.networkPageTitle')"
  >
    <template
      v-if="props.botId && settings && hasChanges"
      #actions
    >
      <span class="text-xs text-muted-foreground">
        {{ $t('common.unsaved') }}
      </span>
    </template>

    <div class="space-y-8">
      <SettingsSection v-if="props.botId">
        <SettingsRow
          :label="$t('bots.settings.networkSDWANSectionTitle')"
          :description="$t('bots.settings.networkSDWANSectionHint')"
        >
          <Switch
            :model-value="form.overlay_enabled"
            @update:model-value="(val) => form.overlay_enabled = val"
          />
        </SettingsRow>

        <template v-if="form.overlay_enabled">
          <SettingsRow :label="$t('bots.settings.overlayProviderFieldLabel')">
            <div class="w-72 shrink-0">
              <OverlayProviderSelect
                v-model="form.overlay_provider"
                :providers="overlayProviderMeta"
                :placeholder="$t('bots.settings.overlayProviderPlaceholder')"
              />
            </div>
          </SettingsRow>

          <!-- One distilled status line; raw diagnostics live behind Details.
               Three mutually-exclusive states of one logical row (pending-save
               / loading / resolved) share SettingsRow's geometry via #content;
               only the resolved state has trailing action buttons. -->
          <SettingsRow v-if="needsSaveForStatus">
            <template #content>
              <div class="flex items-center gap-2.5">
                <span class="size-1.5 shrink-0 rounded-sm bg-warning" />
                <p class="text-sm text-muted-foreground">
                  {{ $t('bots.settings.networkStatusPendingSave') }}
                </p>
              </div>
            </template>
          </SettingsRow>
          <SettingsRow v-else-if="isNetworkStatusLoading && !networkStatusData">
            <template #content>
              <InlineLoadingRow size="md">
                {{ $t('common.loading') }}
              </InlineLoadingRow>
            </template>
          </SettingsRow>
          <SettingsRow v-else-if="networkStatusLine">
            <template #content>
              <div class="flex min-w-0 items-center gap-2.5">
                <span
                  class="size-1.5 shrink-0 rounded-sm"
                  :class="statusDotClass"
                />
                <div class="min-w-0">
                  <div class="truncate text-sm font-medium text-foreground">
                    {{ networkStatusLine.label }}
                  </div>
                  <div
                    v-if="networkStatusLine.detail"
                    class="truncate font-mono text-xs text-muted-foreground"
                  >
                    {{ networkStatusLine.detail }}
                  </div>
                </div>
              </div>
            </template>
            <div class="flex shrink-0 items-center gap-2">
              <Button
                v-if="overlayState === 'needs_login' && overlayAuthURL"
                size="sm"
                variant="outline"
                @click="openAuthURL"
              >
                {{ $t('bots.settings.networkOpenLoginPage') }}
              </Button>
              <Button
                size="sm"
                variant="outline"
                @click="isDetailsDialogOpen = true"
              >
                {{ $t('common.details') }}
              </Button>
            </div>
          </SettingsRow>

          <div v-if="showOverlayConfig">
            <template v-if="primarySchema?.fields?.length">
              <SettingsRow :label="$t('bots.settings.overlayPrimaryConfigTitle')">
                <Button
                  variant="outline"
                  size="sm"
                  @click="isEditorDialogOpen = true"
                >
                  <SquarePen class="size-4" />
                  {{ $t('common.editJson') }}
                </Button>
              </SettingsRow>

              <SettingsRow
                v-for="field in primarySchema.fields"
                :key="field.key"
                :stack="isMultilineField(field) ? 'always' : 'never'"
              >
                <!-- A schema field keeps its Label bound to the control via `for`,
                     so #content carries the label + description rather than the
                     plain-text label prop. A multi-line field stacks the control
                     full-width beneath; a scalar one keeps it beside. -->
                <template #content>
                  <Label
                    :for="`bot-network-config-primary-${field.key}`"
                    class="text-sm font-medium text-foreground"
                  >
                    {{ field.title || field.key }}
                  </Label>
                  <p
                    v-if="field.description"
                    class="mt-0.5 text-xs text-muted-foreground"
                  >
                    {{ field.description }}
                  </p>
                </template>

                <div :class="isMultilineField(field) ? 'w-full' : 'w-full sm:w-80'">
                  <Textarea
                    v-if="isMultilineField(field)"
                    :id="`bot-network-config-primary-${field.key}`"
                    :model-value="stringValue(field)"
                    :placeholder="placeholderOf(field)"
                    :readonly="field.readonly"
                    rows="4"
                    class="min-h-24 resize-none"
                    @update:model-value="(val: string) => updateValue(field.key, val)"
                  />

                  <Switch
                    v-else-if="field.type === 'bool'"
                    :model-value="!!getFieldValue(field)"
                    :disabled="field.readonly"
                    @update:model-value="(val: boolean) => updateValue(field.key, !!val)"
                  />

                  <Select
                    v-else-if="field.type === 'enum' && field.enum"
                    :model-value="stringValue(field)"
                    :disabled="field.readonly"
                    @update:model-value="(val: string) => updateValue(field.key, val)"
                  >
                    <SelectTrigger
                      size="sm"
                      class="w-full"
                    >
                      <SelectValue :placeholder="placeholderOf(field)" />
                    </SelectTrigger>
                    <SelectContent class="w-[--reka-select-trigger-width]">
                      <SelectItem
                        v-for="option in field.enum"
                        :key="option"
                        :value="option"
                      >
                        {{ option }}
                      </SelectItem>
                    </SelectContent>
                  </Select>

                  <InputGroup v-else-if="field.type === 'secret'">
                    <InputGroupInput
                      :id="`bot-network-config-primary-${field.key}`"
                      :model-value="stringValue(field)"
                      :type="visibleSecrets[field.key] ? 'text' : 'password'"
                      :placeholder="placeholderOf(field)"
                      :readonly="field.readonly"
                      @update:model-value="(val: string) => updateValue(field.key, val)"
                    />
                    <InputGroupAddon align="inline-end">
                      <InputGroupButton
                        size="icon-xs"
                        variant="quiet"
                        :aria-label="$t('common.preview')"
                        @click="visibleSecrets[field.key] = !visibleSecrets[field.key]"
                      >
                        <component :is="visibleSecrets[field.key] ? EyeOff : Eye" />
                      </InputGroupButton>
                    </InputGroupAddon>
                  </InputGroup>

                  <Input
                    v-else-if="field.type === 'number'"
                    :id="`bot-network-config-primary-${field.key}`"
                    :model-value="numberValue(field)"
                    type="number"
                    :placeholder="placeholderOf(field)"
                    :readonly="field.readonly"
                    :min="field.constraint?.min"
                    :max="field.constraint?.max"
                    :step="field.constraint?.step ?? 1"
                    class="h-8 w-full tabular-nums"
                    @update:model-value="(val: string) => updateNumber(field.key, val)"
                  />

                  <Input
                    v-else
                    :id="`bot-network-config-primary-${field.key}`"
                    :model-value="stringValue(field)"
                    type="text"
                    :placeholder="placeholderOf(field)"
                    :readonly="field.readonly"
                    class="h-8 w-full"
                    @update:model-value="(val: string) => updateValue(field.key, val)"
                  />
                </div>
              </SettingsRow>
            </template>
          </div>
        </template>
      </SettingsSection>

      <!-- Advanced overlay fields behind a named ActionCard entry opening a
           focused dialog — the house replacement for the old in-card
           "Advanced" expand row. Slim single-line entry (no description, per
           the ActionCard contract); the dialog's DialogDescription carries
           the "all optional" note. -->
      <ActionCard
        v-if="showOverlayConfig && advancedSchema?.fields?.length"
        :title="$t('common.advanced')"
        @click="showAdvancedConfig = true"
      >
        <template #icon>
          <SlidersHorizontal />
        </template>
      </ActionCard>

      <SettingsSection
        v-if="showExitNodeSelector"
        :title="$t('bots.settings.networkExitNode')"
      >
        <SettingsRow
          :label="$t('bots.settings.networkExitNode')"
          :description="nodeListHint"
        >
          <div class="flex w-80 items-center gap-2">
            <NetworkNodeSelect
              v-model="exitNodeValue"
              :nodes="exitNodeOptions"
              :placeholder="$t('bots.settings.networkExitNodePlaceholder')"
            />
            <Button
              variant="outline"
              size="icon-sm"
              :aria-label="$t('common.refresh')"
              loading-mode="icon"
              :loading="isNodeListLoading"
              :disabled="!shouldLoadNodeOptions"
              @click="handleRefreshNodes"
            >
              <RefreshCw class="size-3.5" />
            </Button>
          </div>
        </SettingsRow>
      </SettingsSection>

      <div
        v-if="props.botId && settings && hasChanges"
        class="flex items-center justify-end gap-2"
      >
        <Button
          variant="outline"
          size="sm"
          @click="handleCancel"
        >
          {{ $t('common.cancel') }}
        </Button>
        <Button
          size="sm"
          :loading="isSaving"
          @click="handleSave"
        >
          {{ $t('common.save') }}
        </Button>
      </div>
    </div>

    <!-- Advanced overlay fields dialog (workbench form). The schema-driven
         rows move here UNCHANGED from the old in-card expand — same
         SettingsRow renderer, only the container changed. Draft semantics
         unchanged: edits land in the same config draft; the page's
         Save/Cancel bar remains the commit point. -->
    <Dialog v-model:open="showAdvancedConfig">
      <DialogPanel>
        <DialogHeader>
          <DialogTitle>{{ $t('common.advanced') }}</DialogTitle>
          <DialogDescription>{{ $t('common.allOptional') }}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          <div
            v-for="group in advancedSchemaGroups"
            :key="group.title"
          >
            <div class="mx-4 pb-1 pt-3 text-xs font-medium text-muted-foreground">
              {{ group.title }}
            </div>

            <SettingsRow
              v-for="field in group.fields"
              :key="field.key"
              :stack="isMultilineField(field) ? 'always' : 'never'"
            >
              <template #content>
                <Label
                  :for="`bot-network-config-advanced-${field.key}`"
                  class="text-sm font-medium text-foreground"
                >
                  {{ field.title || field.key }}
                </Label>
                <p
                  v-if="field.description"
                  class="mt-0.5 text-xs text-muted-foreground"
                >
                  {{ field.description }}
                </p>
              </template>

              <div :class="isMultilineField(field) ? 'w-full' : 'w-full sm:w-80'">
                <Textarea
                  v-if="isMultilineField(field)"
                  :id="`bot-network-config-advanced-${field.key}`"
                  :model-value="stringValue(field)"
                  :placeholder="placeholderOf(field)"
                  :readonly="field.readonly"
                  rows="4"
                  class="min-h-24 resize-none"
                  @update:model-value="(val: string) => updateValue(field.key, val)"
                />

                <Switch
                  v-else-if="field.type === 'bool'"
                  :model-value="!!getFieldValue(field)"
                  :disabled="field.readonly"
                  @update:model-value="(val: boolean) => updateValue(field.key, !!val)"
                />

                <Select
                  v-else-if="field.type === 'enum' && field.enum"
                  :model-value="stringValue(field)"
                  :disabled="field.readonly"
                  @update:model-value="(val: string) => updateValue(field.key, val)"
                >
                  <SelectTrigger
                    size="sm"
                    class="w-full"
                  >
                    <SelectValue :placeholder="placeholderOf(field)" />
                  </SelectTrigger>
                  <SelectContent class="w-[--reka-select-trigger-width]">
                    <SelectItem
                      v-for="option in field.enum"
                      :key="option"
                      :value="option"
                    >
                      {{ option }}
                    </SelectItem>
                  </SelectContent>
                </Select>

                <InputGroup v-else-if="field.type === 'secret'">
                  <InputGroupInput
                    :id="`bot-network-config-advanced-${field.key}`"
                    :model-value="stringValue(field)"
                    :type="visibleSecrets[field.key] ? 'text' : 'password'"
                    :placeholder="placeholderOf(field)"
                    :readonly="field.readonly"
                    @update:model-value="(val: string) => updateValue(field.key, val)"
                  />
                  <InputGroupAddon align="inline-end">
                    <InputGroupButton
                      size="icon-xs"
                      variant="quiet"
                      :aria-label="$t('common.preview')"
                      @click="visibleSecrets[field.key] = !visibleSecrets[field.key]"
                    >
                      <component :is="visibleSecrets[field.key] ? EyeOff : Eye" />
                    </InputGroupButton>
                  </InputGroupAddon>
                </InputGroup>

                <Input
                  v-else-if="field.type === 'number'"
                  :id="`bot-network-config-advanced-${field.key}`"
                  :model-value="numberValue(field)"
                  type="number"
                  :placeholder="placeholderOf(field)"
                  :readonly="field.readonly"
                  :min="field.constraint?.min"
                  :max="field.constraint?.max"
                  :step="field.constraint?.step ?? 1"
                  class="h-8 w-full tabular-nums"
                  @update:model-value="(val: string) => updateNumber(field.key, val)"
                />

                <Input
                  v-else
                  :id="`bot-network-config-advanced-${field.key}`"
                  :model-value="stringValue(field)"
                  type="text"
                  :placeholder="placeholderOf(field)"
                  :readonly="field.readonly"
                  class="h-8 w-full"
                  @update:model-value="(val: string) => updateValue(field.key, val)"
                />
              </div>
            </SettingsRow>
          </div>
        </DialogBody>
      </DialogPanel>
    </Dialog>

    <Dialog v-model:open="isEditorDialogOpen">
      <DialogContent class="flex max-h-[calc(100vh-2rem)] flex-col overflow-hidden p-0 sm:h-[70vh] sm:max-w-3xl">
        <DialogHeader class="shrink-0 border-b border-border p-4">
          <DialogTitle class="text-sm font-semibold">
            {{ $t('mcp.editValue') }}
          </DialogTitle>
          <DialogDescription class="text-xs leading-snug">
            {{ $t('mcp.editLongTextHint') }}
          </DialogDescription>
        </DialogHeader>
        
        <div class="flex min-h-0 flex-1 p-4">
          <MonacoEditor
            v-model="editorDraftRaw"
            language="json"
            class="min-h-0 flex-1 rounded-md border border-border"
            :options="{
              automaticLayout: true,
              fixedOverflowWidgets: true,
              minimap: { enabled: false },
              scrollBeyondLastLine: false,
              formatOnPaste: true,
              formatOnType: true
            }"
          />
        </div>

        <DialogFooter class="flex shrink-0 items-center justify-between gap-2 border-t border-border p-4">
          <p
            v-if="editorError"
            class="text-xs text-warning"
          >
            {{ editorError }}
          </p>
          <div v-else />
          <div class="flex items-center gap-2">
            <DialogClose as-child>
              <Button
                variant="ghost"
                size="sm"
              >
                {{ $t('common.cancel') }}
              </Button>
            </DialogClose>
            <Button
              size="sm"
              class="min-w-24"
              :disabled="!!editorError"
              @click="handleEditorSave"
            >
              {{ $t('common.confirm') }}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="isDetailsDialogOpen">
      <DialogContent class="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle class="text-sm font-semibold">
            {{ $t('bots.settings.networkDetailsTitle') }}
          </DialogTitle>
        </DialogHeader>

        <div v-if="overlayNetworkStatusFields.length">
          <div
            v-for="item in overlayNetworkStatusFields"
            :key="`d-ov-${item.label}`"
            class="flex items-start justify-between gap-4 border-b border-border py-2.5 last:border-b-0"
          >
            <span class="shrink-0 text-sm text-muted-foreground">{{ item.label }}</span>
            <span
              class="max-w-[60%] break-all text-right text-sm text-foreground"
              :class="{ 'font-mono': item.mono }"
            >{{ item.value }}</span>
          </div>
        </div>

        <div v-if="workspaceStatusFields.length">
          <div class="mb-1 text-xs font-medium text-muted-foreground">
            {{ $t('bots.settings.networkDetailsWorkspaceGroup') }}
          </div>
          <div
            v-for="item in workspaceStatusFields"
            :key="`d-ws-${item.label}`"
            class="flex items-start justify-between gap-4 border-b border-border py-2.5 last:border-b-0"
          >
            <span class="shrink-0 text-sm text-muted-foreground">{{ item.label }}</span>
            <span
              class="max-w-[60%] break-all text-right text-sm text-foreground"
              :class="{ 'font-mono': item.mono }"
            >{{ item.value }}</span>
          </div>
        </div>

        <DialogFooter
          v-if="showLogoutButton"
          class="border-t border-border pt-4"
        >
          <ConfirmPopover
            variant="destructive"
            :message="$t('bots.settings.networkLogoutConfirm')"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('bots.settings.networkLogout')"
            :loading="isLoggingOut"
            @confirm="handleLogout"
          >
            <template #trigger>
              <Button
                variant="destructive"
                size="sm"
                :loading="isLoggingOut"
              >
                {{ $t('bots.settings.networkLogout') }}
              </Button>
            </template>
          </ConfirmPopover>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </PageShell>
</template>

<script setup lang="ts">
import {
  ActionCard,
  Button,
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
  Label,
  Switch,
  Input,
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
  Textarea,
  Dialog, DialogBody, DialogContent, DialogHeader, DialogPanel, DialogTitle, DialogDescription, DialogFooter, DialogClose,
} from '@felinic/ui'
import { SquarePen, Eye, EyeOff, RefreshCw, SlidersHorizontal } from 'lucide-vue-next'
import { reactive, computed, watch, nextTick, onBeforeUnmount, ref } from 'vue'
import { ConfirmPopover, InlineLoadingRow, PageShell, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import { getBotsByBotIdSettings, putBotsByBotIdSettings } from '@memohai/sdk'
import type { SettingsSettings } from '@memohai/sdk'
import { cloneConfig, getPathValue, setPathValue } from '@/components/config-schema-form/utils'
import type { ConfigSchema, ConfigSchemaField } from '@/components/config-schema-form/types'
import { resolveApiErrorMessage } from '@/utils/api-error'
import OverlayProviderSelect from './network-provider-select.vue'
import NetworkNodeSelect from './network-node-select.vue'
import MonacoEditor from '@/components/monaco-editor/index.vue'
import {
  getBotNetworkStatus,
  executeBotNetworkAction,
  listBotNetworkNodes,
  listOverlayProviderMeta,
  type NetworkBotStatus,
  type OverlayProviderMeta,
} from '@/pages/network/api'

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const queryCache = useQueryCache()

const { data: settings } = useQuery({
  key: () => ['bot-settings', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
})

const { data: overlayProviderMetaData } = useQuery({
  key: ['network-providers-meta'],
  query: () => listOverlayProviderMeta(),
})

const overlayProviderMeta = computed(() => overlayProviderMetaData.value ?? [])

const form = reactive({
  overlay_enabled: false,
  overlay_provider: '',
  overlay_config: {} as Record<string, unknown>,
})

const selectedOverlayProviderMeta = computed(() =>
  overlayProviderMeta.value.find((meta: OverlayProviderMeta) => meta.kind === form.overlay_provider),
)
const selectedNetworkCapabilities = computed(() =>
  selectedOverlayProviderMeta.value?.capabilities ?? null,
)

// Primary and Advanced schemas computed locally to avoid modifying the generic ConfigSchemaForm
const selectedOverlayProviderSchema = computed<ConfigSchema | undefined>(() => {
  const schema = selectedOverlayProviderMeta.value?.config_schema as ConfigSchema | undefined
  if (!schema) return undefined
  return {
    ...schema,
    fields: (schema.fields ?? []).filter(field => field.key !== 'exit_node'),
  }
})

const primarySchema = computed<ConfigSchema | undefined>(() => {
  if (!selectedOverlayProviderSchema.value) return undefined
  return {
    ...selectedOverlayProviderSchema.value,
    fields: selectedOverlayProviderSchema.value.fields.filter(f => !f.collapsed)
  }
})

const advancedSchema = computed<ConfigSchema | undefined>(() => {
  if (!selectedOverlayProviderSchema.value) return undefined
  // Map collapsed to false so the ConfigSchemaForm renders them flat. We handle the wrapper.
  return {
    ...selectedOverlayProviderSchema.value,
    fields: selectedOverlayProviderSchema.value.fields
      .filter(f => f.collapsed)
      .map(f => ({ ...f, collapsed: false }))
  }
})

const advancedSchemaGroups = computed(() => {
  if (!advancedSchema.value) return []
  const fields = advancedSchema.value.fields

  const auth = fields.filter(f => (f.order ?? 0) >= 10 && (f.order ?? 0) < 12)
  const network = fields.filter(f => (f.order ?? 0) >= 12 && (f.order ?? 0) < 20)
  const environment = fields.filter(f => (f.order ?? 0) >= 20 && (f.order ?? 0) < 30)
  const others = fields.filter(f => (f.order ?? 0) >= 30)

  const groups = []
  if (auth.length) groups.push({ title: t('mcp.oauth.title'), fields: auth })
  if (network.length) groups.push({ title: t('sidebar.network'), fields: network })
  if (environment.length) groups.push({ title: t('mcp.env'), fields: environment })
  if (others.length) groups.push({ title: t('common.advanced'), fields: others })

  return groups.map(g => ({
    ...g,
    fields: g.fields
  }))
})

const showOverlayConfig = computed(() =>
  !!form.overlay_enabled
  && !!form.overlay_provider
  && !!selectedOverlayProviderSchema.value?.fields?.length,
)

const showAdvancedConfig = ref(false)

// ---------------------------------------------------------------------------
// Manual Field Rendering Helpers
// ---------------------------------------------------------------------------
const visibleSecrets = reactive<Record<string, boolean>>({})

function getFieldValue(field: ConfigSchemaField) {
  const current = getPathValue(form.overlay_config, field.key)
  if (current !== undefined) return current
  return field.default
}

function stringValue(field: ConfigSchemaField) {
  const value = getFieldValue(field)
  return typeof value === 'string' ? value : value == null ? '' : String(value)
}

function numberValue(field: ConfigSchemaField) {
  const value = getFieldValue(field)
  return typeof value === 'number' ? String(value) : value == null ? '' : String(value)
}

function placeholderOf(field: ConfigSchemaField) {
  let base = field.placeholder || (field.example != null ? String(field.example) : '')

  if (!base) {
    const key = field.key.toLowerCase()
    if (key.includes('key') || key.includes('token') || key.includes('secret')) {
      base = 'tskey-auth-kru6P22CNTRL-...'
    } else if (key.includes('url')) {
      base = 'https://example.com'
    } else if (key.includes('port')) {
      base = '1080'
    } else if (key.includes('host') || key.includes('addr')) {
      base = '192.168.1.1'
    } else if (key.includes('tag')) {
      base = 'tag:bot,tag:server'
    } else if (key.includes('arg') || key.includes('cmd')) {
      base = '--verbose'
    } else if (key.includes('user')) {
      base = 'admin'
    } else if (field.type === 'number') {
      base = '60'
    } else if (field.type === 'textarea') {
      base = t('common.enterContent')
    } else {
      base = '...'
    }
  }

  return t('common.placeholderPrefix', { example: base })
}

function updateValue(path: string, value: unknown) {
  const next = cloneConfig(form.overlay_config)
  setPathValue(next, path, value)
  form.overlay_config = next
}

function updateNumber(path: string, value: string) {
  const nextValue = value === '' ? undefined : Number(value)
  updateValue(path, nextValue)
}

function isMultilineField(field: ConfigSchemaField) {
  return field.type === 'textarea' || field.multiline
}

// Exit node selection only makes sense after the sidecar is authenticated and connected.
const showExitNodeSelector = computed(() =>
  !!form.overlay_enabled
  && !!form.overlay_provider
  && !!selectedNetworkCapabilities.value?.exit_node
  && isConnected.value,
)

const persistedOverlayProvider = computed(() => settings.value?.overlay_provider ?? '')
const persistedOverlayEnabled = computed(() => settings.value?.overlay_enabled ?? false)
const persistedOverlayConfig = computed(() =>
  JSON.stringify((settings.value?.overlay_config as Record<string, unknown> | undefined) ?? {}),
)
const isSelectedNetworkPersisted = computed(() =>
  form.overlay_enabled === persistedOverlayEnabled.value
  && form.overlay_provider === persistedOverlayProvider.value
  && JSON.stringify(form.overlay_config ?? {}) === persistedOverlayConfig.value,
)
const shouldLoadNetworkStatus = computed(() =>
  !!props.botId
  && persistedOverlayEnabled.value
  && !!persistedOverlayProvider.value,
)
const shouldLoadNodeOptions = computed(() =>
  !!props.botId
  && shouldLoadNetworkStatus.value
  && !!selectedNetworkCapabilities.value?.exit_node,
)

// Transient states that should trigger automatic polling until resolved.
const TRANSIENT_STATES = ['starting', 'needs_login', 'needslogin', 'stopped']

const isTransientState = computed(() =>
  TRANSIENT_STATES.includes(overlayState.value),
)

const {
  data: networkStatusData,
  refetch: refetchNetworkStatus,
  isFetching: isNetworkStatusFetching,
  isLoading: isNetworkStatusLoading,
} = useQuery({
  key: () => ['bot-network-status', props.botId],
  query: () => getBotNetworkStatus(props.botId),
  enabled: () => !!props.botId,
  refetchOnWindowFocus: true,
})

const {
  data: nodeListData,
  isLoading: isNodeListLoading,
  refetch: refetchNodeList,
} = useQuery({
  key: () => ['bot-network-nodes', props.botId, persistedOverlayProvider.value],
  query: () => listBotNetworkNodes(props.botId),
  enabled: () => shouldLoadNodeOptions.value,
})

const { mutateAsync: updateSettings, isLoading: isSaving } = useMutation({
  mutation: async (body: Partial<SettingsSettings>) => {
    const { data } = await putBotsByBotIdSettings({
      path: { bot_id: props.botId },
      body,
      throwOnError: true,
    })
    return data
  },
  onSettled: () => {
    queryCache.invalidateQueries({ key: ['bot-settings', props.botId] })
    queryCache.invalidateQueries({ key: ['bot-network-status', props.botId] })
    queryCache.invalidateQueries({ key: ['bot-network-nodes', props.botId] })
  },
})

const { mutateAsync: runNetworkAction, isLoading: isLoggingOut } = useMutation({
  mutation: (actionID: string) =>
    executeBotNetworkAction(props.botId, actionID, {}),
  onSettled: () => {
    queryCache.invalidateQueries({ key: ['bot-network-status', props.botId] })
  },
})

// ---------------------------------------------------------------------------
// Editor state
// ---------------------------------------------------------------------------
const isEditorDialogOpen = ref(false)
const isDetailsDialogOpen = ref(false)
const editorDraftRaw = ref('')
const editorError = ref('')

watch(isEditorDialogOpen, (open) => {
  if (open) {
    editorDraftRaw.value = JSON.stringify(form.overlay_config, null, 2)
    editorError.value = ''
  }
})

watch(editorDraftRaw, (val) => {
  try {
    JSON.parse(val)
    editorError.value = ''
  } catch {
    editorError.value = t('mcp.importErrorJson')
  }
})

function handleEditorSave() {
  try {
    const parsed = JSON.parse(editorDraftRaw.value)
    form.overlay_config = cloneConfig(parsed)
    isEditorDialogOpen.value = false
  } catch {
    // Should be caught by watcher, but just in case
    editorError.value = t('mcp.importErrorJson')
  }
}

// ---------------------------------------------------------------------------
// Overlay state helpers
// ---------------------------------------------------------------------------

const overlayState = computed(() => {
  const status = networkStatusData.value as NetworkBotStatus | null
  return status?.state ?? ''
})

const overlayAuthURL = computed(() => {
  const status = networkStatusData.value as NetworkBotStatus | null
  return (status?.details?.auth_url as string | undefined) ?? ''
})

// "Connected" means sidecar is fully running and authenticated.
const isConnected = computed(() =>
  ['ready', 'running', 'degraded'].includes(overlayState.value),
)

// Show logout when the sidecar is alive (connected or waiting for login).
const showLogoutButton = computed(() =>
  shouldLoadNetworkStatus.value
  && !needsSaveForStatus.value
  && ['ready', 'running', 'degraded', 'needs_login', 'starting', 'stopped'].includes(overlayState.value),
)

// ---------------------------------------------------------------------------

const exitNodeOptions = computed(() =>
  (nodeListData.value?.items ?? []).filter(node => node.can_exit_node !== false),
)
const nodeListHint = computed(() => {
  if (!isSelectedNetworkPersisted.value) return t('bots.settings.networkNodesPendingSave')
  if (nodeListData.value?.message) return nodeListData.value.message
  if (!exitNodeOptions.value.length) return t('bots.settings.networkNodesEmpty')
  return t('bots.settings.networkExitNodeDescription')
})
const exitNodeValue = computed({
  get: () => String(form.overlay_config.exit_node ?? ''),
  set: (value: string) => {
    form.overlay_config = {
      ...form.overlay_config,
      exit_node: value || undefined,
    }
  },
})

const needsSaveForStatus = computed(() =>
  form.overlay_enabled
  && !!form.overlay_provider
  && !isSelectedNetworkPersisted.value,
)

const showOverlayStatusInNetworkCard = computed(() =>
  shouldLoadNetworkStatus.value
  && !!networkStatusData.value,
)

type NetworkStatusTone = 'success' | 'warning' | 'muted' | 'destructive'

// Distill the backend's many states into one human line + a colored dot; the raw
// fields live behind Details (§13: let one status carry the surface, hide diagnostics).
const networkStatusLine = computed<{ tone: NetworkStatusTone; label: string; detail?: string } | null>(() => {
  if (!showOverlayStatusInNetworkCard.value) return null
  const status = networkStatusData.value as NetworkBotStatus | null
  const details = status?.details ?? {}
  const address = status?.network_ip || (details.dns_name as string | undefined) || ''
  switch (overlayState.value) {
    case 'ready':
    case 'running':
      return { tone: 'success', label: t('bots.settings.networkState.connected'), detail: address || undefined }
    case 'degraded':
      return { tone: 'warning', label: t('bots.settings.networkState.degraded'), detail: address || undefined }
    case 'needs_login':
    case 'needslogin':
      return { tone: 'warning', label: t('bots.settings.networkState.needsLogin') }
    case 'starting':
      return { tone: 'muted', label: t('bots.settings.networkState.connecting') }
    case 'stopped':
      return { tone: 'muted', label: t('bots.settings.networkState.stopped') }
    default:
      return { tone: 'muted', label: overlayState.value || t('bots.settings.networkState.unknown') }
  }
})

const statusDotClass = computed(() => {
  switch (networkStatusLine.value?.tone) {
    case 'success': return 'bg-success'
    case 'warning': return 'bg-warning'
    case 'destructive': return 'bg-destructive'
    default: return 'bg-muted-foreground'
  }
})

function workspaceStateDisplay(state: string) {
  const key = `bots.settings.networkWorkspaceState.${state}`
  const translated = t(key)
  return translated === key ? t('bots.settings.networkWorkspaceState.unknown') : translated
}

const workspaceStatusFields = computed(() => {
  const status = networkStatusData.value
  if (!status || !status.workspace) return []
  const ws = status.workspace
  const items: { label: string; value: string; mono: boolean }[] = [
    { label: t('bots.settings.networkWorkspaceStateLabel'), value: workspaceStateDisplay(ws.state), mono: false },
  ]
  if (ws.container_id) items.push({ label: t('bots.settings.networkWorkspaceContainerID'), value: ws.container_id, mono: true })
  if (ws.task_status) items.push({ label: t('bots.settings.networkWorkspaceTaskStatus'), value: ws.task_status, mono: false })
  if (ws.pid != null && ws.pid > 0) {
    items.push({ label: t('bots.settings.networkWorkspaceTaskPID'), value: String(ws.pid), mono: true })
  }
  if (ws.network_target) items.push({ label: t('bots.settings.networkWorkspaceTarget'), value: ws.network_target, mono: true })
  if (ws.message) items.push({ label: t('bots.settings.networkWorkspaceMessage'), value: ws.message, mono: false })
  return items.filter(item => item.value)
})

const overlayNetworkStatusFields = computed(() => {
  const status = networkStatusData.value
  if (!status) return []
  const details = status.details ?? {}
  const items: { label: string; value: string | undefined; mono: boolean }[] = [
    { label: t('bots.settings.networkStatusState'), value: status.state, mono: false },
    { label: t('bots.settings.networkStatusIP'), value: status.network_ip, mono: true },
    { label: t('bots.settings.networkStatusProxy'), value: status.proxy_address, mono: true },
    { label: t('bots.settings.networkStatusPID'), value: details.pid == null ? undefined : String(details.pid), mono: true },
    { label: t('bots.settings.networkStatusDNSName'), value: details.dns_name as string | undefined, mono: true },
    { label: t('bots.settings.networkStatusBackendState'), value: details.backend_state as string | undefined, mono: false },
    { label: t('bots.settings.networkStatusHealth'), value: Array.isArray(details.health) ? details.health.join('; ') : undefined, mono: false },
    { label: t('bots.settings.networkStatusSocket'), value: details.localapi_socket_host_path as string | undefined, mono: true },
    { label: t('bots.settings.networkStatusExitNode'), value: details.configured_exit_node as string | undefined, mono: true },
  ]
  return items.filter(item => item.value)
})

const hasChanges = computed(() => {
  if (!settings.value) return true
  const s = settings.value
  return form.overlay_enabled !== (s.overlay_enabled ?? false)
    || form.overlay_provider !== (s.overlay_provider ?? '')
    || JSON.stringify(form.overlay_config ?? {}) !== JSON.stringify((s.overlay_config as Record<string, unknown> | undefined) ?? {})
})

// When settings load from API, overlay_provider goes from '' to the saved value in the
// same flush as configs are written. A separate watcher on overlay_provider must not
// wipe those configs (it would leave the UI empty after refresh).
let skipProviderChangeReset = false

watch(() => form.overlay_provider, (next, prev) => {
  if (next === prev || skipProviderChangeReset) return
  form.overlay_config = {}
})

watch(settings, (val) => {
  if (!val) return
  skipProviderChangeReset = true
  form.overlay_enabled = val.overlay_enabled ?? false
  form.overlay_provider = val.overlay_provider ?? ''
  form.overlay_config = cloneConfig((val.overlay_config as Record<string, unknown> | undefined) ?? {})
  void nextTick(() => {
    skipProviderChangeReset = false
  })
}, { immediate: true })

// Poll network status every 5s while in a transient state (starting, needs_login, etc.)
let pollTimer: ReturnType<typeof setInterval> | null = null

watch(isTransientState, (shouldPoll) => {
  if (shouldPoll && !pollTimer) {
    pollTimer = setInterval(() => {
      if (isTransientState.value && !isNetworkStatusFetching.value) {
        refetchNetworkStatus()
      }
    }, 5000)
  } else if (!shouldPoll && pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}, { immediate: true })

onBeforeUnmount(() => {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
})

async function handleSave() {
  if (form.overlay_enabled && !form.overlay_provider) {
    toast.error(t('bots.settings.overlayProviderRequired'))
    return
  }
  try {
    await updateSettings({
      overlay_enabled: form.overlay_enabled,
      overlay_provider: form.overlay_provider,
      overlay_config: form.overlay_config,
    })
    toast.success(t('bots.settings.saveSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.settings.networkActionFailed')))
  }
}

function handleCancel() {
  if (settings.value) {
    skipProviderChangeReset = true
    form.overlay_enabled = settings.value.overlay_enabled ?? false
    form.overlay_provider = settings.value.overlay_provider ?? ''
    form.overlay_config = cloneConfig((settings.value.overlay_config as Record<string, unknown> | undefined) ?? {})
    void nextTick(() => {
      skipProviderChangeReset = false
    })
  }
}

async function handleRefreshNodes() {
  try {
    await refetchNodeList()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.settings.networkNodesRefreshFailed')))
  }
}

function openAuthURL() {
  if (overlayAuthURL.value) {
    window.open(overlayAuthURL.value, '_blank', 'noopener,noreferrer')
  }
}

async function handleLogout() {
  try {
    await runNetworkAction('logout')
    toast.success(t('bots.settings.networkLogoutSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.settings.networkLogoutFailed')))
  }
}
</script>

<style scoped>
</style>
