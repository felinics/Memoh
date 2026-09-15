<!-- eslint-disable vue/no-mutating-props -->
<template>
  <SettingsSection :title="$t('bots.settings.blocks.multimedia')">
    <SettingsRow
      v-for="row in rows"
      :key="row.field"
      :label="$t(row.labelKey)"
      stack="sm"
    >
      <div class="flex w-full justify-end sm:w-56">
        <ModelSelect
          v-if="row.models.length > 0 || form[row.field]"
          v-model="form[row.field]"
          :models="row.models"
          :providers="row.providers"
          :model-type="row.modelType"
          :placeholder="$t(row.placeholderKey)"
          :none-label="row.clearable ? $t('common.none') : undefined"
        />
        <!-- A select whose only option is "None" is a dead end; when no model
             exists, the row becomes a doorway to the page that creates one. -->
        <Button
          v-else
          variant="outline"
          size="sm"
          @click="openModelSettings(row.routeName)"
        >
          <Plus />
          {{ $t('models.addModel') }}
        </Button>
      </div>
    </SettingsRow>
  </SettingsSection>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { Plus } from 'lucide-vue-next'
import { Button, SettingsRow, SettingsSection } from '@felinic/ui'
import ModelSelect from './model-select.vue'
import type {
  SettingsSettings,
  AudioSpeechModelResponse,
  AudioSpeechProviderResponse,
  AudioTranscriptionModelResponse,
  ModelsGetResponse,
  ModelsModelType,
  ProvidersGetResponse,
  VideoProviderResponse,
} from '@memohai/sdk'

interface MultimediaRow {
  field: 'tts_model_id' | 'transcription_model_id' | 'image_model_id' | 'video_model_id'
  labelKey: string
  placeholderKey: string
  models: ModelsGetResponse[]
  providers: ProvidersGetResponse[]
  modelType: ModelsModelType
  // clearable rows offer an explicit "None" entry (voice models), the others
  // treat an empty value as unset and don't need it.
  clearable?: boolean
  routeName: 'voice' | 'providers' | 'video'
}

function toModelOptions(
  models: AudioSpeechModelResponse[] | AudioTranscriptionModelResponse[],
  type: 'speech' | 'transcription',
): ModelsGetResponse[] {
  return models.map((m) => ({
    id: m.id,
    model_id: m.model_id,
    name: m.name,
    provider_id: m.provider_id,
    type,
  }))
}

function toProviderOptions(
  providers: AudioSpeechProviderResponse[],
): ProvidersGetResponse[] {
  return providers.map((p) => ({
    id: p.id,
    name: p.name,
    icon: p.icon,
    enable: p.enable,
    client_type: p.client_type,
    config: p.config,
    created_at: p.created_at,
    updated_at: p.updated_at,
  }))
}

const props = defineProps<{
  form: SettingsSettings
  ttsModels: AudioSpeechModelResponse[]
  ttsProviders: AudioSpeechProviderResponse[]
  transcriptionModels: AudioTranscriptionModelResponse[]
  transcriptionProviders: AudioSpeechProviderResponse[]
  imageCapableModels: ModelsGetResponse[]
  providers: ProvidersGetResponse[]
  videoModels: ModelsGetResponse[]
  videoProviders: VideoProviderResponse[]
}>()

const router = useRouter()

const rows = computed<MultimediaRow[]>(() => [
  {
    field: 'tts_model_id',
    labelKey: 'bots.settings.ttsModel',
    placeholderKey: 'bots.settings.ttsModelPlaceholder',
    models: toModelOptions(props.ttsModels, 'speech'),
    providers: toProviderOptions(props.ttsProviders),
    modelType: 'speech',
    clearable: true,
    routeName: 'voice',
  },
  {
    field: 'transcription_model_id',
    labelKey: 'bots.settings.transcriptionModel',
    placeholderKey: 'bots.settings.transcriptionModelPlaceholder',
    models: toModelOptions(props.transcriptionModels, 'transcription'),
    providers: toProviderOptions(props.transcriptionProviders),
    modelType: 'transcription',
    clearable: true,
    routeName: 'voice',
  },
  {
    field: 'image_model_id',
    labelKey: 'bots.settings.imageModel',
    placeholderKey: 'bots.settings.imageModelPlaceholder',
    models: props.imageCapableModels,
    providers: props.providers,
    modelType: 'chat',
    routeName: 'providers',
  },
  {
    field: 'video_model_id',
    labelKey: 'bots.settings.videoModel',
    placeholderKey: 'bots.settings.videoModelPlaceholder',
    models: props.videoModels,
    providers: props.videoProviders,
    modelType: 'video',
    routeName: 'video',
  },
])

function openModelSettings(routeName: MultimediaRow['routeName']): void {
  void router.push({ name: routeName })
}
</script>
