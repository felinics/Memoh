import { computed, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import type { HandlersSiteIconResponse } from '@memohai/sdk'
import { useSettingsStore } from '@/store/settings'
import { lookupSiteIcon } from '@/utils/site-icon'

// Resolves the favicon for one link. `ready` stays false until the image has
// actually decoded, so callers keep their placeholder icon instead of showing
// an empty gap while a slow favicon loads or after it fails.
export function useSiteIcon(origin: MaybeRefOrGetter<string | null>) {
  const settings = useSettingsStore()
  const icons = ref<HandlersSiteIconResponse | null>(null)
  watch(() => toValue(origin), async (value, _, onCleanup) => {
    icons.value = null
    if (!value) return
    let stale = false
    onCleanup(() => { stale = true })
    const result = await lookupSiteIcon(value)
    if (!stale) icons.value = result
  }, { immediate: true })

  const colorScheme = computed(() => settings.resolvedColorMode)
  const src = computed(() => icons.value?.[colorScheme.value] || '')
  // Keyed by src, so a theme switch to the other icon starts from "not loaded".
  const loaded = ref('')
  const failed = ref('')
  return {
    src,
    colorScheme,
    usable: computed(() => !!src.value && failed.value !== src.value),
    ready: computed(() => !!src.value && loaded.value === src.value && failed.value !== src.value),
    onLoad: (event: Event) => { loaded.value = (event.target as HTMLImageElement).src },
    onError: (event: Event) => { failed.value = (event.target as HTMLImageElement).src },
  }
}
