<template>
  <div class="mx-auto max-w-5xl px-4 py-6 md:px-6 md:py-8">
    <InlineLoadingRow
      v-if="loading"
      class="justify-center py-16"
    >
      {{ $t('common.loading') }}
    </InlineLoadingRow>

    <div
      v-else-if="!pkg"
      class="py-16 text-center"
    >
      <p class="text-sm font-medium">
        {{ $t('supermarket.packageNotFound') }}
      </p>
      <Button
        variant="outline"
        size="sm"
        class="mt-4"
        @click="router.push({ name: 'supermarket' })"
      >
        <ArrowLeft class="size-4" />
        {{ $t('supermarket.backToSupermarket') }}
      </Button>
    </div>

    <template v-else>
      <MarketDetailHeader
        :name="pkg.name || pkg.package_id"
        :tags="pkg.tags"
        @back="router.push({ name: 'supermarket' })"
        @install="installDialogOpen = true"
      >
        <template #icon>
          <SkillIcon
            :icon="pkg.icon"
            variant="detail"
          />
        </template>
      </MarketDetailHeader>

      <p class="mt-8 max-w-4xl text-base leading-7 text-muted-foreground">
        {{ pkg.description || $t('supermarket.noDescription') }}
      </p>

      <section class="mt-8">
        <h2 class="mb-4 text-lg font-semibold">
          {{ $t('supermarket.includedSkills') }}
        </h2>
        <SettingsSection>
          <SettingsRow
            v-for="skill in pkg.skills"
            :key="skill.skill_id"
          >
            <template #leading>
              <div class="flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-md border bg-background">
                <SkillIcon :icon="skill.icon" />
              </div>
            </template>
            <template #content>
              <p class="text-sm font-medium">
                {{ skill.name || skill.skill_id }}
              </p>
              <p class="mt-1 text-xs text-muted-foreground">
                {{ skill.description }}
              </p>
            </template>
          </SettingsRow>
        </SettingsSection>
      </section>

      <section class="mt-10">
        <h2 class="text-lg font-semibold">
          {{ $t('supermarket.information') }}
        </h2>
        <div class="mt-4 grid gap-x-12 gap-y-5 md:grid-cols-2">
          <InfoItem
            :label="$t('supermarket.registry')"
            :value="registryName || pkg.registry_id"
          />
          <InfoItem
            :label="$t('supermarket.category')"
            :value="categoryNames || $t('common.none')"
          />
        </div>
      </section>
    </template>

    <InstallPackageDialog
      v-model:open="installDialogOpen"
      :pkg="pkg"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeft } from 'lucide-vue-next'
import { Button, InlineLoadingRow, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import {
  getSupermarketRegistries,
  getSupermarketRegistriesByRegistryIdPackagesByPackageId,
  getSupermarketRegistriesByRegistryIdPackagesByPackageIdReleasesByRevision,
  type HandlersSupermarketSkillPackageDescriptor,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import InfoItem from './components/info-item.vue'
import InstallPackageDialog from './components/install-package-dialog.vue'
import MarketDetailHeader from './components/market-detail-header.vue'
import SkillIcon from './components/skill-icon.vue'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const pkg = ref<HandlersSupermarketSkillPackageDescriptor | null>(null)
const registryName = ref('')
const loading = ref(false)
const installDialogOpen = ref(false)
const registryId = computed(() => String(route.params.registryId || ''))
const packageId = computed(() => String(route.params.packageId || ''))
const revision = computed(() => String(route.query.revision || ''))
const packageIdentity = computed(() => `${registryId.value}/${packageId.value}/${revision.value}`)
const categoryNames = computed(() => pkg.value?.categories.map(category => category.name).join(', ') || '')
let loadSequence = 0

async function loadPackage() {
  if (!registryId.value || !packageId.value) return
  const sequence = ++loadSequence
  loading.value = true
  pkg.value = null
  try {
    const packageRequest = revision.value
      ? getSupermarketRegistriesByRegistryIdPackagesByPackageIdReleasesByRevision({
          path: { registry_id: registryId.value, package_id: packageId.value, revision: revision.value },
          throwOnError: true,
        })
      : getSupermarketRegistriesByRegistryIdPackagesByPackageId({
          path: { registry_id: registryId.value, package_id: packageId.value },
          throwOnError: true,
        })
    const [{ data }, registryResponse] = await Promise.all([
      packageRequest,
      getSupermarketRegistries({ throwOnError: true }).catch(() => null),
    ])
    if (sequence !== loadSequence) return
    pkg.value = data
    registryName.value = registryResponse?.data.data
      ?.find(registry => registry.id === registryId.value)?.name || registryId.value
  } catch (error) {
    if (sequence !== loadSequence) return
    pkg.value = null
    registryName.value = registryId.value
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  } finally {
    if (sequence === loadSequence) loading.value = false
  }
}

onMounted(loadPackage)
watch(packageIdentity, loadPackage)
</script>
