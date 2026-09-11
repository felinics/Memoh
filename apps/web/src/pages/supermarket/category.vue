<template>
  <DetailPane
    width="narrow"
    :back-label="$t('sidebar.supermarket')"
    @back="goBack"
  >
    <SettingsShell width="narrow">
      <PageHeader
        :title="title"
        :level="2"
        framed
      />

      <InlineLoadingRow
        v-if="loading"
        class="justify-center py-8"
      >
        {{ $t('common.loading') }}
      </InlineLoadingRow>

      <div
        v-else-if="!apps.length"
        class="py-8 text-center text-xs text-muted-foreground"
      >
        {{ $t('supermarket.noAppResults') }}
      </div>

      <div
        v-else
        class="grid grid-cols-1 gap-4 sm:grid-cols-2"
      >
        <AppCard
          v-for="pkg in apps"
          :key="`${pkg.registry_id}/${pkg.app_id}`"
          :pkg="pkg"
          :bot-id="botId"
        />
      </div>

      <div
        v-if="showPagination"
        class="mt-6 flex justify-end gap-2"
      >
        <Button
          variant="outline"
          size="icon-sm"
          :disabled="page === 1 || loading"
          :aria-label="$t('supermarket.previousPage')"
          @click="page--"
        >
          <ChevronLeft class="size-4" />
        </Button>
        <Button
          variant="outline"
          size="icon-sm"
          :disabled="!hasNextPage || loading"
          :aria-label="$t('supermarket.nextPage')"
          @click="page++"
        >
          <ChevronRight class="size-4" />
        </Button>
      </div>
    </SettingsShell>
  </DetailPane>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ChevronLeft, ChevronRight } from 'lucide-vue-next'
import { Button, DetailPane, InlineLoadingRow, PageHeader, SettingsShell, toast } from '@felinic/ui'
import { getSupermarketApps, type HandlersSupermarketAppSummary } from '@memohai/sdk'
import { categoryDisplayName, useAppCategoriesQuery } from '@/composables/api/useApps'
import { resolveApiErrorMessage } from '@/utils/api-error'
import AppCard from './components/app-card.vue'

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const pageSize = 50

const page = ref(1)
const total = ref(0)
const apps = ref<HandlersSupermarketAppSummary[]>([])
const loading = ref(false)

const categoryId = computed(() => String(route.params.categoryId ?? ''))
const registryId = computed(() => (typeof route.query.registry === 'string' ? route.query.registry : ''))
const botId = computed(() => (typeof route.query.botId === 'string' ? route.query.botId : ''))

const categoriesQuery = useAppCategoriesQuery()
const category = computed(() => (categoriesQuery.data.value ?? []).find(item => item.id === categoryId.value))
// The table may still be loading; the id is a readable fallback until then.
const title = computed(() => categoryDisplayName(category.value, category.value?.name || categoryId.value, locale.value))

const hasNextPage = computed(() => page.value * pageSize < total.value)
const showPagination = computed(() => page.value > 1 || hasNextPage.value)

function goBack() {
  router.push({ name: 'supermarket', query: botId.value ? { botId: botId.value } : undefined })
}

let sequence = 0
async function loadApps() {
  const current = ++sequence
  loading.value = true
  try {
    const { data } = await getSupermarketApps({
      query: {
        registry: registryId.value || undefined,
        category: categoryId.value,
        page: page.value,
        limit: pageSize,
        sort: 'relevance',
      },
      throwOnError: true,
    })
    if (current !== sequence) return
    apps.value = data.data ?? []
    total.value = data.total ?? 0
  } catch (error) {
    if (current !== sequence) return
    apps.value = []
    total.value = 0
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  } finally {
    if (current === sequence) loading.value = false
  }
}

watch([categoryId, registryId], () => {
  if (page.value !== 1) {
    page.value = 1
    return
  }
  void loadApps()
}, { immediate: true })
watch(page, loadApps)
</script>
