<script setup lang="ts">
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useSettingsStore } from '@/store/settings'
import { useOnboarding } from '@/composables/useOnboarding'
import { Button, FieldStack, Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@felinic/ui'
import { Moon, Sun } from 'lucide-vue-next'
import ColorSchemeCard from '@/components/color-scheme-card/index.vue'
import { colorSchemes } from '@/constants/color-schemes'
import type { Locale } from '@/i18n'
import { useStepTransition } from '../useStepTransition'
import StepFrame from '../components/step-frame.vue'
import StepExitShell from '../components/step-exit-shell.vue'
import FooterNav from '../components/footer-nav.vue'

const { t } = useI18n()
const settingsStore = useSettingsStore()
const { language, theme, colorScheme } = storeToRefs(settingsStore)
const { setLanguage, setTheme, setColorScheme } = settingsStore
const { nextStep, prevStep } = useOnboarding()
const { visible, exiting, leave } = useStepTransition()
</script>

<template>
  <StepExitShell :exiting="exiting">
    <StepFrame
      :title="t('onboarding.appearance.title')"
      title-class="mb-8"
      :visible="visible"
    >
      <template #default>
        <div class="min-h-0 flex-1 overflow-y-auto -mx-2 px-2 -my-1 py-1 space-y-6">
          <div
            class="transition-all duration-[350ms] ease-out delay-[80ms]"
            :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
          >
            <FieldStack :label="t('settings.language')">
              <Select
                :model-value="language"
                @update:model-value="(value) => value && setLanguage(value as Locale)"
              >
                <SelectTrigger class="w-full">
                  <SelectValue :placeholder="t('settings.languagePlaceholder')" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="zh">
                    {{ t('settings.langZh') }}
                  </SelectItem>
                  <SelectItem value="en">
                    {{ t('settings.langEn') }}
                  </SelectItem>
                  <SelectItem value="ja">
                    {{ t('settings.langJa') }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </FieldStack>
          </div>

          <div
            class="transition-all duration-[350ms] ease-out delay-[160ms]"
            :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
          >
            <FieldStack :label="t('settings.theme')">
              <div class="flex gap-2">
                <Button
                  type="button"
                  variant="outline"
                  class="justify-start gap-2"
                  :data-ui-selected="theme === 'light' ? '' : undefined"
                  @click="setTheme('light')"
                >
                  <Sun class="size-4" />
                  {{ t('settings.themeLight') }}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  class="justify-start gap-2"
                  :data-ui-selected="theme === 'dark' ? '' : undefined"
                  @click="setTheme('dark')"
                >
                  <Moon class="size-4" />
                  {{ t('settings.themeDark') }}
                </Button>
              </div>
            </FieldStack>
          </div>

          <div
            class="transition-all duration-[350ms] ease-out delay-[240ms]"
            :class="visible ? 'opacity-100 translate-y-0' : 'opacity-0 -translate-y-3'"
          >
            <FieldStack :label="t('settings.appearance.colorScheme')">
              <div class="grid grid-cols-3 gap-3">
                <ColorSchemeCard
                  v-for="scheme in colorSchemes"
                  :key="scheme.id"
                  :scheme="scheme"
                  :selected="colorScheme === scheme.id"
                  @select="setColorScheme(scheme.id)"
                />
              </div>
            </FieldStack>
          </div>
        </div>

        <FooterNav
          class="delay-[320ms]"
          :visible="visible"
          :prev-label="t('onboarding.prev')"
          :next-label="t('onboarding.next')"
          @prev="leave(prevStep)"
          @next="leave(nextStep)"
        />
      </template>
    </StepFrame>
  </StepExitShell>
</template>
