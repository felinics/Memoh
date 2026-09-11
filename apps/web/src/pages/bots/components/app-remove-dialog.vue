<script setup lang="ts">
// Confirms an App removal with the Server's plan: which dependencies are
// really removed (versus kept because another App shares them or the
// image ships them), which connections are disconnected, and which
// auto-installed Apps would lose their last reference.
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Alert,
  AlertDescription,
  Button,
  Checkbox,
  Dialog,
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  InlineLoadingRow,
  Label,
} from '@felinic/ui'
import type { AppRemovalPreview } from '@/composables/api/useApps'

const props = defineProps<{
  open: boolean
  name: string
  preview: AppRemovalPreview | null
  loading: boolean
  error: string
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
  confirm: [options: { removeUnreferencedRequired: boolean }]
}>()

const { t } = useI18n()
const removeRequired = ref(false)

watch(() => props.open, (open) => {
  if (open) removeRequired.value = false
})

const removedDependencies = computed(() => (props.preview?.dependencies ?? []).filter(dep => dep.action === 'remove'))
const keptDependencies = computed(() => (props.preview?.dependencies ?? []).filter(dep => dep.action === 'keep'))
const disconnected = computed(() => (props.preview?.connectors ?? []).filter(conn => conn.action === 'disconnect'))
const keptConnectors = computed(() => (props.preview?.connectors ?? []).filter(conn => conn.action === 'keep'))
const dependencyCount = computed(() => removedDependencies.value.length + keptDependencies.value.length)
const connectorCount = computed(() => disconnected.value.length + keptConnectors.value.length)
const requiredApps = computed(() => props.preview?.required_apps ?? [])

function reasonLabel(reason?: string): string {
  switch (reason) {
    case 'shared':
      return t('apps.remove.reason.shared')
    case 'image':
      return t('apps.remove.reason.image')
    case 'absent':
      return t('apps.remove.reason.absent')
    default:
      return ''
  }
}
</script>

<template>
  <Dialog
    :open="open"
    @update:open="(value) => emit('update:open', value)"
  >
    <DialogPanel
      width="lg"
      footer
    >
      <DialogHeader class="min-w-0">
        <DialogTitle class="break-words">
          {{ t('apps.remove.title', { name }) }}
        </DialogTitle>
        <DialogDescription class="break-words">
          {{ t('apps.remove.description', { name }) }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody class="min-w-0 space-y-4">
        <InlineLoadingRow v-if="loading">
          {{ t('common.loading') }}
        </InlineLoadingRow>
        <Alert
          v-else-if="error"
          variant="destructive"
        >
          <AlertDescription>{{ error }}</AlertDescription>
        </Alert>
        <template v-else-if="preview">
          <section v-if="removedDependencies.length || keptDependencies.length">
            <h4 class="mb-1 text-caption font-medium uppercase tracking-wide text-muted-foreground">
              {{ t('apps.section.dependencies', { count: dependencyCount }, dependencyCount) }}
            </h4>
            <ul class="space-y-1 text-body">
              <li
                v-for="dep in removedDependencies"
                :key="dep.id"
                class="flex items-center justify-between gap-2"
              >
                <span class="font-mono">{{ dep.id }}</span>
                <span class="text-destructive">{{ t('apps.remove.willRemove') }}</span>
              </li>
              <li
                v-for="dep in keptDependencies"
                :key="dep.id"
                class="flex items-center justify-between gap-2 text-muted-foreground"
              >
                <span class="font-mono">{{ dep.id }}</span>
                <span>{{ t('apps.remove.willKeep') }} · {{ reasonLabel(dep.reason) }}</span>
              </li>
            </ul>
          </section>

          <section v-if="disconnected.length || keptConnectors.length">
            <h4 class="mb-1 text-caption font-medium uppercase tracking-wide text-muted-foreground">
              {{ t('apps.section.connectors', { count: connectorCount }, connectorCount) }}
            </h4>
            <ul class="space-y-1 text-body">
              <li
                v-for="conn in disconnected"
                :key="conn.type"
                class="flex items-center justify-between gap-2"
              >
                <span class="font-mono">{{ conn.type }}</span>
                <span class="text-destructive">{{ t('apps.remove.willDisconnect') }}</span>
              </li>
              <li
                v-for="conn in keptConnectors"
                :key="conn.type"
                class="flex items-center justify-between gap-2 text-muted-foreground"
              >
                <span class="font-mono">{{ conn.type }}</span>
                <span>{{ t('apps.remove.willKeep') }} · {{ reasonLabel(conn.reason) }}</span>
              </li>
            </ul>
          </section>

          <section v-if="requiredApps.length">
            <div class="flex items-start gap-2">
              <Checkbox
                id="app-remove-required"
                :model-value="removeRequired"
                @update:model-value="(value) => { removeRequired = value === true }"
              />
              <Label
                for="app-remove-required"
                class="text-body font-normal"
              >
                {{ t('apps.remove.alsoRemoveRequired') }}
                <span class="mt-0.5 block font-mono text-caption text-muted-foreground">
                  {{ requiredApps.map(pkg => pkg.app_id).join(', ') }}
                </span>
              </Label>
            </div>
          </section>
        </template>
      </DialogBody>

      <DialogFooter class="min-w-0 items-center gap-2">
        <Button
          variant="outline"
          @click="emit('update:open', false)"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          variant="destructive"
          :disabled="loading || !!error"
          @click="emit('confirm', { removeUnreferencedRequired: removeRequired })"
        >
          {{ t('apps.action.remove') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
