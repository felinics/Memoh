<script setup lang="ts">
import { computed, provide, reactive, ref, watch } from 'vue'
import { useQuery, useQueryCache } from '@pinia/colada'
import { BackendCard, Button, DetailPane, PageShell, SectionGroup, SwapTransition } from '@felinic/ui'
import { getProviderTemplates, getSpeechProviders, getTranscriptionProviders, postSpeechProvidersByIdImportModels, postTranscriptionProvidersByIdImportModels } from '@memohai/sdk'
import type { AudioSpeechProviderResponse, ProvidersGetResponse, ProvidertemplatesGetResponse } from '@memohai/sdk'
import { Plus } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import AddProvider from '@/components/add-provider/index.vue'
import ProviderIcon from '@/components/provider-icon/index.vue'
import { useRoutedViewSwap } from '@/composables/useViewSwap'
import SpeechSetting from '@/pages/speech/components/provider-setting.vue'
import TranscriptionSetting from '@/pages/transcription/provider-setting.vue'
import { isTemplateConfigured, providerDraftFromTemplate } from '@/utils/provider-template'

const { t } = useI18n()
const queryCache = useQueryCache()

const { data: speechData, isLoading: speechLoading } = useQuery({
  key: () => ['speech-providers'],
  query: async () => {
    const { data } = await getSpeechProviders({ throwOnError: true })
    return data
  },
})
const { data: transcriptionData, isLoading: transcriptionLoading } = useQuery({
  key: () => ['transcription-providers'],
  query: async () => {
    const { data } = await getTranscriptionProviders({ throwOnError: true })
    return (data ?? []) as AudioSpeechProviderResponse[]
  },
})

const { data: speechTemplateData, isLoading: speechTemplatesLoading } = useQuery({
  key: () => ['provider-templates', 'speech'],
  query: async () => {
    const { data } = await getProviderTemplates({ query: { domain: 'speech' }, throwOnError: true })
    return data
  },
})

const { data: transcriptionTemplateData, isLoading: transcriptionTemplatesLoading } = useQuery({
  key: () => ['provider-templates', 'transcription'],
  query: async () => {
    const { data } = await getProviderTemplates({ query: { domain: 'transcription' }, throwOnError: true })
    return data
  },
})

type TemplateAudioProvider = AudioSpeechProviderResponse & {
  provider_template_id?: string
}

const curTts = ref<TemplateAudioProvider>()
const curTranscription = ref<TemplateAudioProvider>()
const optimisticSpeechProvider = ref<TemplateAudioProvider>()
const optimisticTranscriptionProvider = ref<TemplateAudioProvider>()
provide('curTtsProvider', curTts)
provide('curTranscriptionProvider', curTranscription)

type VoiceDetailKind = 'speech' | 'transcription'
type VoiceDetail = { kind: VoiceDetailKind, provider: TemplateAudioProvider }
const detailKind = ref<VoiceDetailKind>('speech')
const openStatus = reactive({ addSpeechOpen: false, addTranscriptionOpen: false })
const initialSpeechTemplateId = ref('')
const initialTranscriptionTemplateId = ref('')

async function importSpeechModels(providerId: string) {
  const { data } = await postSpeechProvidersByIdImportModels({
    path: { id: providerId },
    throwOnError: true,
  })
  queryCache.invalidateQueries({ key: ['speech-providers'] })
  queryCache.invalidateQueries({ key: ['speech-models'] })
  queryCache.invalidateQueries({ key: ['speech-provider-models', providerId] })
  return data
}

async function importTranscriptionModels(providerId: string) {
  const { data } = await postTranscriptionProvidersByIdImportModels({
    path: { id: providerId },
    throwOnError: true,
  })
  queryCache.invalidateQueries({ key: ['transcription-providers'] })
  queryCache.invalidateQueries({ key: ['transcription-models'] })
  queryCache.invalidateQueries({ key: ['transcription-provider-models', providerId] })
  return data
}

function sortByEnabled<T extends { enable?: boolean }>(list: T[]) {
  return [...list].sort((a, b) => Number(b.enable !== false) - Number(a.enable !== false))
}

const speechProviders = computed<AudioSpeechProviderResponse[]>(() =>
  Array.isArray(speechData.value) ? sortByEnabled(speechData.value) : [],
)
const transcriptionProviders = computed<AudioSpeechProviderResponse[]>(() =>
  Array.isArray(transcriptionData.value) ? sortByEnabled(transcriptionData.value) : [],
)
const speechTemplates = computed<ProvidertemplatesGetResponse[]>(() =>
  Array.isArray(speechTemplateData.value) ? speechTemplateData.value : [],
)
const transcriptionTemplates = computed<ProvidertemplatesGetResponse[]>(() =>
  Array.isArray(transcriptionTemplateData.value) ? transcriptionTemplateData.value : [],
)
const availableSpeechTemplates = computed(() => speechTemplates.value.filter(template =>
  !isTemplateConfigured(template)
  && template.id !== optimisticSpeechProvider.value?.provider_template_id,
))
const availableTranscriptionTemplates = computed(() => transcriptionTemplates.value.filter(template =>
  !isTemplateConfigured(template)
  && template.id !== optimisticTranscriptionProvider.value?.provider_template_id,
))
const catalogSpeechProviders = computed<TemplateAudioProvider[]>(() => {
  const provider = optimisticSpeechProvider.value
  if (!provider?.id || speechProviders.value.some(item => item.id === provider.id)) return speechProviders.value
  return sortByEnabled([provider, ...speechProviders.value])
})
const catalogTranscriptionProviders = computed<TemplateAudioProvider[]>(() => {
  const provider = optimisticTranscriptionProvider.value
  if (!provider?.id || transcriptionProviders.value.some(item => item.id === provider.id)) return transcriptionProviders.value
  return sortByEnabled([provider, ...transcriptionProviders.value])
})
const speechTemplateDrafts = computed<TemplateAudioProvider[]>(() =>
  availableSpeechTemplates.value.map(template => providerDraftFromTemplate(template) as TemplateAudioProvider),
)
const transcriptionTemplateDrafts = computed<TemplateAudioProvider[]>(() =>
  availableTranscriptionTemplates.value.map(template => providerDraftFromTemplate(template) as TemplateAudioProvider),
)

// Page-owned query key, valued `kind:id` so refresh restores which pane.
const {
  view,
  direction,
  isDetailLoading,
  openDetail,
  backToList: closeProvider,
} = useRoutedViewSwap<VoiceDetail>({
  key: 'voiceProvider',
  items: () => [
    ...catalogSpeechProviders.value.map(provider => ({ kind: 'speech' as const, provider })),
    ...speechTemplateDrafts.value.map(provider => ({ kind: 'speech' as const, provider })),
    ...catalogTranscriptionProviders.value.map(provider => ({ kind: 'transcription' as const, provider })),
    ...transcriptionTemplateDrafts.value.map(provider => ({ kind: 'transcription' as const, provider })),
  ],
  selected: () => {
    const provider = detailKind.value === 'speech' ? curTts.value : curTranscription.value
    return provider ? { kind: detailKind.value, provider } : undefined
  },
  select: (detail) => {
    detailKind.value = detail?.kind ?? 'speech'
    curTts.value = detail?.kind === 'speech' ? detail.provider : undefined
    curTranscription.value = detail?.kind === 'transcription' ? detail.provider : undefined
  },
  getRouteValue: detail => `${detail.kind}:${detail.provider.provider_template_id
    ? `template:${detail.provider.provider_template_id}`
    : detail.provider.id ?? ''}`,
  isLoading: (routeValue) => {
    if (routeValue.startsWith('speech:template:')) {
      return speechTemplatesLoading.value || speechLoading.value
    }
    if (routeValue.startsWith('transcription:template:')) {
      return transcriptionTemplatesLoading.value || transcriptionLoading.value
    }
    if (routeValue.startsWith('speech:')) return speechLoading.value
    if (routeValue.startsWith('transcription:')) return transcriptionLoading.value
    return false
  },
  isReady: (routeValue) => {
    if (routeValue.startsWith('speech:template:')) {
      return speechTemplateData.value !== undefined && speechData.value !== undefined
    }
    if (routeValue.startsWith('transcription:template:')) {
      return transcriptionTemplateData.value !== undefined && transcriptionData.value !== undefined
    }
    if (routeValue.startsWith('speech:')) return speechData.value !== undefined
    if (routeValue.startsWith('transcription:')) return transcriptionData.value !== undefined
    // Malformed / unknown kind — ready to reject immediately.
    return true
  },
})

const addProviderNames = computed(() => [
  ...catalogSpeechProviders.value.map((p) => ({ name: p.name })),
  ...catalogTranscriptionProviders.value.map((p) => ({ name: p.name })),
])

function getInitials(name: string | undefined) {
  const label = name?.trim() ?? ''
  return label ? label.slice(0, 2).toUpperCase() : '?'
}

function openSpeech(provider: TemplateAudioProvider) {
  openDetail({ kind: 'speech', provider })
}

function openTranscription(provider: TemplateAudioProvider) {
  openDetail({ kind: 'transcription', provider })
}

function openAddSpeech(templateId?: string) {
  initialSpeechTemplateId.value = templateId ?? ''
  openStatus.addSpeechOpen = true
}

function openAddTranscription(templateId?: string) {
  initialTranscriptionTemplateId.value = templateId ?? ''
  openStatus.addTranscriptionOpen = true
}

function handleSpeechMaterialized(provider: ProvidersGetResponse) {
  const result = provider as TemplateAudioProvider
  optimisticSpeechProvider.value = result
  openDetail({ kind: 'speech', provider: result })
}

function handleTranscriptionMaterialized(provider: ProvidersGetResponse) {
  const result = provider as TemplateAudioProvider
  optimisticTranscriptionProvider.value = result
  openDetail({ kind: 'transcription', provider: result })
}

// Each section adds its own kind of provider, so refresh just that list when
// the matching add dialog closes.
watch(() => openStatus.addSpeechOpen, (isOpen, wasOpen) => {
  if (wasOpen && !isOpen) {
    queryCache.invalidateQueries({ key: ['speech-providers'] })
  }
})
watch(() => openStatus.addTranscriptionOpen, (isOpen, wasOpen) => {
  if (wasOpen && !isOpen) {
    queryCache.invalidateQueries({ key: ['transcription-providers'] })
  }
})
</script>

<template>
  <SwapTransition :direction="direction">
    <!-- Two capability sections -->
    <PageShell
      v-if="view === 'list'"
      :title="t('voice.title')"
    >
      <div class="space-y-8">
        <!-- Speaking (TTS) -->
        <SectionGroup
          :title="t('voice.speakTitle')"
          :description="t('voice.speakHint')"
        >
          <template #actions>
            <Button
              variant="secondary"
              size="sm"
              @click="openAddSpeech()"
            >
              <Plus class="size-4" />
              {{ t('common.add') }}
            </Button>
          </template>

          <div
            v-if="catalogSpeechProviders.length + speechTemplateDrafts.length > 0"
            class="grid grid-cols-1 gap-3 sm:grid-cols-2"
          >
            <BackendCard
              v-for="provider in catalogSpeechProviders"
              :key="provider.id"
              :name="provider.name ?? ''"
              :enabled="provider.enable !== false"
              @click="openSpeech(provider)"
            >
              <template #leading>
                <span class="flex size-10 items-center justify-center rounded-full bg-muted">
                  <ProviderIcon
                    v-if="provider.icon"
                    :icon="provider.icon"
                    size="1.5em"
                  />
                  <span
                    v-else
                    class="text-xs font-medium text-muted-foreground"
                  >
                    {{ getInitials(provider.name) }}
                  </span>
                </span>
              </template>
            </BackendCard>
            <BackendCard
              v-for="provider in speechTemplateDrafts"
              :key="`template:${provider.provider_template_id}`"
              :name="provider.name ?? ''"
              @click="openSpeech(provider)"
            >
              <template #leading>
                <span class="flex size-10 items-center justify-center rounded-full bg-muted">
                  <ProviderIcon
                    v-if="provider.icon"
                    :icon="provider.icon"
                    size="1.5em"
                  />
                  <span
                    v-else
                    class="text-xs font-medium text-muted-foreground"
                  >
                    {{ getInitials(provider.name) }}
                  </span>
                </span>
              </template>
            </BackendCard>
          </div>
          <p
            v-else
            class="px-2 text-xs text-muted-foreground"
          >
            {{ t('voice.empty') }}
          </p>
        </SectionGroup>

        <!-- Listening (STT) -->
        <SectionGroup
          :title="t('voice.listenTitle')"
          :description="t('voice.listenHint')"
        >
          <template #actions>
            <Button
              variant="secondary"
              size="sm"
              @click="openAddTranscription()"
            >
              <Plus class="size-4" />
              {{ t('common.add') }}
            </Button>
          </template>

          <div
            v-if="catalogTranscriptionProviders.length + transcriptionTemplateDrafts.length > 0"
            class="grid grid-cols-1 gap-3 sm:grid-cols-2"
          >
            <BackendCard
              v-for="provider in catalogTranscriptionProviders"
              :key="provider.id"
              :name="provider.name ?? ''"
              :enabled="provider.enable !== false"
              @click="openTranscription(provider)"
            >
              <template #leading>
                <span class="flex size-10 items-center justify-center rounded-full bg-muted">
                  <ProviderIcon
                    v-if="provider.icon"
                    :icon="provider.icon"
                    size="1.5em"
                  />
                  <span
                    v-else
                    class="text-xs font-medium text-muted-foreground"
                  >
                    {{ getInitials(provider.name) }}
                  </span>
                </span>
              </template>
            </BackendCard>
            <BackendCard
              v-for="provider in transcriptionTemplateDrafts"
              :key="`template:${provider.provider_template_id}`"
              :name="provider.name ?? ''"
              @click="openTranscription(provider)"
            >
              <template #leading>
                <span class="flex size-10 items-center justify-center rounded-full bg-muted">
                  <ProviderIcon
                    v-if="provider.icon"
                    :icon="provider.icon"
                    size="1.5em"
                  />
                  <span
                    v-else
                    class="text-xs font-medium text-muted-foreground"
                  >
                    {{ getInitials(provider.name) }}
                  </span>
                </span>
              </template>
            </BackendCard>
          </div>
          <p
            v-else
            class="px-2 text-xs text-muted-foreground"
          >
            {{ t('voice.empty') }}
          </p>
        </SectionGroup>
      </div>

      <AddProvider
        v-model:open="openStatus.addSpeechOpen"
        hide-trigger
        preset-domain="speech"
        :providers="addProviderNames"
        :templates="speechTemplates"
        :initial-template-id="initialSpeechTemplateId"
        :import-models="importSpeechModels"
      />
      <AddProvider
        v-model:open="openStatus.addTranscriptionOpen"
        hide-trigger
        preset-domain="transcription"
        :providers="addProviderNames"
        :templates="transcriptionTemplates"
        :initial-template-id="initialTranscriptionTemplateId"
        :import-models="importTranscriptionModels"
      />
    </PageShell>

    <!-- Voice backend detail -->
    <DetailPane
      v-else
      width="narrow"
      :back-label="t('voice.title')"
      :loading="isDetailLoading || !(detailKind === 'speech' ? curTts : curTranscription)"
      @back="closeProvider"
    >
      <SpeechSetting
        v-if="detailKind === 'speech' && curTts"
        @materialized="handleSpeechMaterialized"
      />
      <TranscriptionSetting
        v-else-if="detailKind === 'transcription' && curTranscription"
        @materialized="handleTranscriptionMaterialized"
      />
    </DetailPane>
  </SwapTransition>
</template>
