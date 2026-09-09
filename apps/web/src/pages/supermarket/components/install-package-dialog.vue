<template>
  <Dialog
    :open="open"
    @update:open="$emit('update:open', $event)"
  >
    <DialogContent class="sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>{{ $t('supermarket.packageInstallTitle') }}</DialogTitle>
      </DialogHeader>
      <div class="space-y-4 py-2">
        <FieldStack :label="$t('supermarket.selectBot')">
          <BotSelect
            v-model="selectedBotId"
            trigger-class="w-full"
          />
        </FieldStack>
        <div
          v-if="pkg"
          class="space-y-1 rounded-md border border-border p-3"
        >
          <p class="text-xs font-medium">
            {{ pkg.name || pkg.package_id }}
          </p>
          <p class="line-clamp-3 text-xs text-muted-foreground">
            {{ pkg.description }}
          </p>
        </div>
      </div>
      <DialogFooter>
        <DialogClose as-child>
          <Button
            variant="outline"
            :disabled="installing"
          >
            {{ $t('common.cancel') }}
          </Button>
        </DialogClose>
        <Button
          :disabled="!selectedBotId || !pkg?.revision || !pkg.skills.length"
          :loading="installing"
          @click="handleInstall"
        >
          {{ installing ? $t('supermarket.installing') : $t('supermarket.install') }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Button, Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle, FieldStack, toast,
} from '@felinic/ui'
import {
  postBotsByBotIdSupermarketInstallPackage,
  type HandlersSupermarketSkillPackageDescriptor,
} from '@memohai/sdk'
import BotSelect from '@/components/bot-select/index.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{
  open: boolean
  pkg: HandlersSupermarketSkillPackageDescriptor | null
}>()
const emit = defineEmits<{
  'update:open': [open: boolean]
  'installed': []
}>()
const { t } = useI18n()
const selectedBotId = ref('')
const installing = ref(false)

watch(() => props.open, (open) => {
  if (!open) {
    selectedBotId.value = ''
    installing.value = false
  }
})

async function handleInstall() {
  if (!selectedBotId.value || !props.pkg?.registry_id || !props.pkg.package_id || !props.pkg.revision) return
  const botID = selectedBotId.value
  const registryID = props.pkg.registry_id
  const packageID = props.pkg.package_id
  const revision = props.pkg.revision
  installing.value = true
  try {
    await postBotsByBotIdSupermarketInstallPackage({
      path: { bot_id: botID },
      body: {
        registry_id: registryID,
        package_id: packageID,
        revision,
      },
      throwOnError: true,
    })
    toast.success(t('supermarket.installSuccess'))
    emit('update:open', false)
    emit('installed')
    emit('update:open', false)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('supermarket.installFailed')))
  } finally {
    installing.value = false
  }
}

</script>
