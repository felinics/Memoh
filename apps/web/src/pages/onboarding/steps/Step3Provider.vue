<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Spinner,
} from '@felinic/ui'
import { ArrowLeft, Plus, AlertCircle } from 'lucide-vue-next'
import { FieldStack, FormStack } from '@felinic/ui'
import { useOnboarding } from '@/composables/useOnboarding'
import ProviderIcon from '@/components/provider-icon/index.vue'
import CreateModel from '@/components/create-model/index.vue'
import ModelItem from '@/pages/providers/components/model-item.vue'
import ChoiceTile from '../components/choice-tile.vue'
import StepFrame from '../components/step-frame.vue'
import StepExitShell from '../components/step-exit-shell.vue'
import FooterNav from '../components/footer-nav.vue'
import { onboardingProviderPresets as providerPresets, type ProviderPreset } from '@/constants/provider-presets'
import { useStepTransition, nextFrame } from '../useStepTransition'
import { readOnboardingProviderId, writeOnboardingProviderId } from '../session'
import { useProviderSetup } from './useProviderSetup'

const { t } = useI18n()
const { nextStep, prevStep } = useOnboarding()
const { visible, exiting, leave } = useStepTransition()
const listVisible = ref(false)
const gridVisible = ref(false)
const mode = ref<'list' | 'form'>('list')
const formVisible = ref(false)
const formContentVisible = ref(false)
const selectedPreset = ref<ProviderPreset | null>(null)
const hasConfiguredProvider = ref(!!readOnboardingProviderId())

function advanceWithProvider(result: { providerId: string }) {
  writeOnboardingProviderId(result.providerId)
  hasConfiguredProvider.value = true
  leave(nextStep)
}

const {
  formValues, formError,
  createdProviderId, errorState, errorDetail, manualMode,
  importing, submitting, deleteModelLoading,
  providerModels,
  availableClientTypes, baseUrlPlaceholder,
  formCtaLabel, formCtaDisabled,
  resetFormState, initFormValues, clearSuppressDirtyReset,
  saveAndNext, onRetry, onEnterManual, openAddDialog,
  handleEditModel, handleDeleteModel,
} = useProviderSetup({
  selectedPreset: () => selectedPreset.value,
  onProviderReady: advanceWithProvider,
})

const ctaLabel = computed(() =>
  hasConfiguredProvider.value
    ? t('onboarding.next')
    : t('onboarding.skip'),
)

function openForm(preset: ProviderPreset | null) {
  selectedPreset.value = preset
  initFormValues(preset)
  listVisible.value = false
  setTimeout(() => {
    mode.value = 'form'
    formVisible.value = false
    formContentVisible.value = false
    nextFrame(() => {
      formVisible.value = true
      formContentVisible.value = true
      clearSuppressDirtyReset()
    })
  }, 175)
}

function backToList() {
  formVisible.value = false
  formContentVisible.value = false
  setTimeout(() => {
    mode.value = 'list'
    selectedPreset.value = null
    resetFormState()
    listVisible.value = false
    visible.value = false
    nextFrame(() => {
      listVisible.value = true
      visible.value = true
    })
  }, 175)
}

function onSkipStep() {
  leave(nextStep)
}

onMounted(() => {
  nextFrame(() => {
    listVisible.value = true
    gridVisible.value = true
  })

  if (import.meta.env.DEV) {
    ;(window as unknown as Record<string, unknown>).__step3 = {
      showError(kind: 'http' | 'unreachable' | 'authError' | 'noModels' = 'noModels') {
        createdProviderId.value = 'mock-provider-id'
        errorState.value = kind
        manualMode.value = false
        console.info(`[step3] error state -> ${kind}`)
      },
      showManual() {
        createdProviderId.value = 'mock-provider-id'
        errorState.value = null
        manualMode.value = true
        console.info('[step3] manual mode (use real API for adds, models won\'t persist with mock id)')
      },
      openAddDialog: openAddDialog,
      reset() {
        resetFormState()
        console.info('[step3] reset')
      },
      state() {
        return {
          mode: mode.value,
          createdProviderId: createdProviderId.value,
          errorState: errorState.value,
          manualMode: manualMode.value,
          providerModels: providerModels.value,
          importing: importing.value,
        }
      },
    }
    console.info('[step3] dev helpers: __step3.showError("http"|"unreachable"|"authError"|"noModels"), __step3.showManual(), __step3.openAddDialog(), __step3.reset()')
  }
})
</script>

<template>
  <!-- 两个模式变体共用一个退出壳,所以壳在 StepFrame 之上而非各 frame 自带 -->
  <StepExitShell :exiting="exiting">
    <StepFrame
      v-if="mode === 'list'"
      :title="t('onboarding.provider.title')"
      title-class="mb-3"
      :visible="visible"
      :body-class="['pt-16 transition-all duration-[175ms] ease-out', listVisible ? 'scale-100 opacity-100' : 'scale-[0.96] opacity-0']"
    >
      <div>
        <p
          class="text-sm text-muted-foreground leading-relaxed mb-6 transition-all duration-[350ms] ease-out delay-[60ms]"
          :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
        >
          {{ t('onboarding.provider.description') }}
        </p>
        <div
          class="grid grid-cols-3 gap-3 transition-all duration-[350ms] ease-out delay-[140ms]"
          :class="gridVisible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
        >
          <ChoiceTile
            variant="dashed"
            :label="t('onboarding.provider.custom')"
            @click="openForm(null)"
          >
            <template #icon>
              <Plus class="size-5 shrink-0" />
            </template>
          </ChoiceTile>
          <ChoiceTile
            v-for="preset in providerPresets"
            :key="preset.id"
            :label="preset.name"
            @click="openForm(preset)"
          >
            <template #icon>
              <ProviderIcon
                v-if="preset.icon"
                :icon="preset.icon"
                size="22"
              />
            </template>
          </ChoiceTile>
        </div>
      </div>

      <FooterNav
        class="delay-[220ms]"
        :visible="visible"
        :prev-label="t('onboarding.prev')"
        :next-label="ctaLabel"
        @prev="leave(prevStep)"
        @next="onSkipStep"
      />
    </StepFrame>

    <StepFrame
      v-else-if="mode === 'form'"
      :visible="visible"
      :body-class="['pt-16 transition-all duration-[175ms] ease-out', formVisible ? 'scale-100 opacity-100' : 'scale-[0.96] opacity-0']"
    >
      <!-- form 模式有返回箭头+图标+标题的头行,而非纯 h2,走 #header slot -->
      <template #header>
        <div
          class="mb-8 flex items-center gap-3 transition-all duration-[200ms] ease-out"
          :class="formContentVisible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
        >
          <button
            type="button"
            class="-ml-1.5 inline-flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            :disabled="submitting"
            :aria-label="t('onboarding.prev')"
            @click="backToList"
          >
            <ArrowLeft class="size-4" />
          </button>
          <ProviderIcon
            v-if="selectedPreset?.icon"
            :icon="selectedPreset.icon"
            size="28"
          />
          <h2 class="text-2xl font-semibold">
            {{ selectedPreset ? selectedPreset.name : t('onboarding.provider.custom') }}
          </h2>
        </div>
      </template>

      <div class="min-h-0 flex-1 overflow-y-auto -mx-2 px-2 -my-1 py-1">
        <FormStack>
          <div
            class="transition-all duration-[200ms] ease-out delay-[20ms]"
            :class="formContentVisible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
          >
            <FieldStack>
              <template #label>
                <Label class="text-sm font-medium">
                  {{ t('onboarding.provider.form.name') }}
                </Label>
              </template>
              <Input
                v-model="formValues.name"
                :placeholder="t('onboarding.provider.form.namePlaceholder')"
              />
            </FieldStack>
          </div>
          <div
            v-if="!selectedPreset"
            class="transition-all duration-[200ms] ease-out delay-[40ms]"
            :class="formContentVisible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
          >
            <FieldStack>
              <template #label>
                <Label class="text-sm font-medium">
                  {{ t('onboarding.provider.form.clientType') }}
                </Label>
              </template>
              <Select v-model="formValues.client_type">
                <SelectTrigger class="w-full">
                  <SelectValue :placeholder="t('onboarding.provider.form.clientTypePlaceholder')" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem
                    v-for="ct in availableClientTypes"
                    :key="ct.value"
                    :value="ct.value"
                  >
                    {{ ct.label }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </FieldStack>
          </div>
          <div
            class="transition-all duration-[200ms] ease-out"
            :class="[
              formContentVisible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3',
              selectedPreset ? 'delay-[40ms]' : 'delay-[60ms]',
            ]"
          >
            <FieldStack>
              <template #label>
                <Label class="text-sm font-medium">
                  {{ t('onboarding.provider.form.apiKey') }}
                </Label>
              </template>
              <Input
                v-model="formValues.api_key"
                type="password"
                autocomplete="off"
                :placeholder="t('onboarding.provider.form.apiKeyPlaceholder')"
              />
            </FieldStack>
          </div>
          <div
            class="transition-all duration-[200ms] ease-out"
            :class="[
              formContentVisible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3',
              selectedPreset ? 'delay-[60ms]' : 'delay-[80ms]',
            ]"
          >
            <FieldStack>
              <template #label>
                <Label class="text-sm font-medium">
                  {{ t('onboarding.provider.form.baseUrl') }}
                </Label>
              </template>
              <Input
                v-model="formValues.base_url"
                :placeholder="baseUrlPlaceholder"
              />
            </FieldStack>
          </div>
        </FormStack>

        <p
          v-if="formError"
          class="mt-3 text-xs text-destructive"
        >
          {{ formError }}
        </p>

        <div
          v-if="errorState"
          class="mt-5 rounded-lg border border-destructive-border bg-destructive-soft p-4"
        >
          <div class="flex items-start gap-3">
            <AlertCircle class="size-5 shrink-0 text-destructive mt-0.5" />
            <div class="flex-1">
              <p class="text-sm font-medium text-destructive">
                {{ errorState === 'unreachable'
                  ? t('onboarding.provider.form.errorUnreachableTitle')
                  : errorState === 'authError'
                    ? t('onboarding.provider.form.errorAuthTitle')
                    : errorState === 'noModels'
                      ? t('onboarding.provider.form.errorNoModelsTitle')
                      : t('onboarding.provider.form.errorHttpTitle') }}
              </p>
              <p class="mt-1 text-xs text-muted-foreground leading-relaxed">
                {{ errorState === 'unreachable'
                  ? t('onboarding.provider.form.errorUnreachableDescription')
                  : errorState === 'authError'
                    ? t('onboarding.provider.form.errorAuthDescription')
                    : errorState === 'noModels'
                      ? t('onboarding.provider.form.errorNoModelsDescription')
                      : t('onboarding.provider.form.errorHttpDescription') }}
              </p>
              <p
                v-if="errorDetail"
                class="mt-1 text-xs text-muted-foreground/70 font-mono"
              >
                {{ errorDetail }}
              </p>
              <div class="mt-3 flex items-center gap-2">
                <button
                  type="button"
                  class="inline-flex h-8 items-center justify-center rounded-md border border-border bg-background px-3 text-xs font-medium transition-colors hover:bg-accent disabled:opacity-50 disabled:cursor-not-allowed"
                  :disabled="importing"
                  @click="onRetry"
                >
                  {{ t('onboarding.provider.form.retry') }}
                </button>
                <button
                  v-if="errorState !== 'unreachable' && errorState !== 'authError'"
                  type="button"
                  class="inline-flex h-8 items-center justify-center rounded-md border border-border bg-background px-3 text-xs font-medium transition-colors hover:bg-accent disabled:opacity-50 disabled:cursor-not-allowed"
                  :disabled="importing || !createdProviderId"
                  @click="onEnterManual"
                >
                  {{ t('onboarding.provider.form.manualAdd') }}
                </button>
              </div>
            </div>
          </div>
        </div>

        <div
          v-if="errorState && createdProviderId"
          class="mt-2 text-right"
        >
          <button
            type="button"
            class="text-xs text-muted-foreground transition-colors hover:text-foreground underline-offset-2 hover:underline"
            @click="onSkipStep"
          >
            {{ t('onboarding.provider.form.skipStep') }}
          </button>
        </div>

        <div
          v-if="manualMode && createdProviderId"
          class="mt-6"
        >
          <div class="flex items-center justify-between mb-3">
            <h4 class="text-sm font-semibold">
              {{ t('models.title') }}
              <span
                v-if="providerModels.length > 0"
                class="ml-2 text-xs font-normal text-muted-foreground"
              >
                {{ providerModels.length }}
              </span>
            </h4>
            <CreateModel :id="createdProviderId" />
          </div>

          <div
            v-if="providerModels.length === 0"
            class="rounded-lg border border-dashed border-border py-8 text-center"
          >
            <p class="text-sm text-muted-foreground">
              {{ t('models.emptyTitle') }}
            </p>
            <p class="mt-1 text-xs text-muted-foreground">
              {{ t('onboarding.provider.form.manualAddEmpty') }}
            </p>
          </div>

          <div
            v-else
            class="grid gap-3 grid-cols-1 sm:grid-cols-2"
          >
            <ModelItem
              v-for="model in providerModels"
              :key="model.id || `${model.provider_id}:${model.model_id}`"
              :model="model"
              :delete-loading="deleteModelLoading"
              @edit="handleEditModel"
              @delete="handleDeleteModel"
            />
          </div>
        </div>
      </div>

      <FooterNav
        :class="selectedPreset ? 'delay-[80ms]' : 'delay-[100ms]'"
        :visible="formContentVisible"
      >
        <template #prev>
          <button
            type="button"
            class="inline-flex h-[2.625rem] items-center justify-center rounded-lg px-4 text-sm font-normal text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50 disabled:cursor-not-allowed"
            :disabled="submitting"
            @click="backToList"
          >
            {{ t('onboarding.provider.form.cancel') }}
          </button>
        </template>
        <template #next>
          <!-- CTA label carries its own Transition + Spinner; FooterNav's
               plain :next-loading prop can't express the label-swap, so this
               button stays local via the #next escape hatch. -->
          <button
            type="button"
            class="inline-flex h-[2.625rem] w-[180px] items-center justify-center rounded-lg bg-primary px-5 font-normal text-primary-foreground shadow-none transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:opacity-60 disabled:cursor-not-allowed"
            :disabled="formCtaDisabled"
            @click="saveAndNext"
          >
            <Transition
              mode="out-in"
              enter-active-class="transition-all duration-[160ms] ease-out"
              enter-from-class="opacity-0 translate-y-1"
              enter-to-class="opacity-100 translate-y-0"
              leave-active-class="transition-all duration-[140ms] ease-in"
              leave-from-class="opacity-100 translate-y-0"
              leave-to-class="opacity-0 -translate-y-1"
            >
              <span
                :key="formCtaLabel"
                class="inline-flex items-center gap-2"
              >
                <Spinner v-if="submitting" />
                {{ formCtaLabel }}
              </span>
            </Transition>
          </button>
        </template>
      </FooterNav>
    </StepFrame>
  </StepExitShell>
</template>
