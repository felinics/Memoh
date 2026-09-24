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
  // A one-color icon that would vanish on this scheme comes back as a mask,
  // painted in the link color; it is inline, so there is nothing to wait for.
  const mask = computed(() => (colorScheme.value === 'dark' ? icons.value?.dark_mask : icons.value?.light_mask) || '')
  // Keyed by src, so a theme switch to the other icon starts from "not loaded".
  // The <img> is keyed by src too, so an event always belongs to the current
  // src; recording the element's own src would compare the browser-normalized
  // URL (lowercased host, dropped :443, encoded spaces) against the raw one.
  const loaded = ref('')
  const failed = ref('')
  return {
    src,
    mask,
    colorScheme,
    usable: computed(() => !mask.value && !!src.value && failed.value !== src.value),
    ready: computed(() => !!mask.value || (!!src.value && loaded.value === src.value && failed.value !== src.value)),
    onLoad: () => { loaded.value = src.value },
    onError: () => { failed.value = src.value },
  }
}
