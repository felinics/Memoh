// Dev-only state for comparing mobile visual directions on the real app.
//
// A direction is a preset of a palette (token overrides) and a platform
// skeleton (scoped geometry rules), both in mobile-directions.css; the press
// feel is an independent axis (press-feel.ts) so any direction can be tried
// with any feel. All are carried as attributes on <html> — the same mechanism
// as data-color-scheme — so no page or component has to know a direction
// exists.
//
// The navigation form and the sent-bubble motion are further axes: they are
// not styles, so they drive the app's own switches (mobileNavMode,
// bubbleSpringEnabled) instead of root attributes.
//
// Values come from localStorage, and `?direction=` / `?feel=` / `?nav=` /
// `?bubble=` in the URL override and persist them, so a phone on the LAN can
// open a specific combination from a link without touching the switcher.
import { ref, watch, type Ref } from 'vue'
import { bubbleSpringEnabled } from '@/pages/home/bubble-motion'
import { mobileNavMode, type MobileNavMode } from '@/pages/main-section/mobile-nav-mode'

export const mobileDirections = [
  { value: 'baseline', label: 'Current' },
  { value: 'mono', label: 'Mono' },
  { value: 'mono-ios', label: 'Mono · iOS' },
  { value: 'ios', label: 'iOS' },
  { value: 'material', label: 'Material 3' },
] as const

type Palette = 'mono' | 'ios' | 'material'
type Platform = 'ios' | 'material'

const DIRECTION_PRESETS: Record<Exclude<MobileDirection, 'baseline'>, { palette: Palette, platform: Platform }> = {
  'mono': { palette: 'mono', platform: 'material' },
  'mono-ios': { palette: 'mono', platform: 'ios' },
  'ios': { palette: 'ios', platform: 'ios' },
  'material': { palette: 'material', platform: 'material' },
}

export const pressFeels = [
  { value: 'baseline', label: 'Current' },
  { value: 'ripple', label: 'Ripple' },
  { value: 'ios', label: 'iOS' },
] as const

export const navForms = [
  { value: 'sheet', label: 'Sheet' },
  { value: 'pager', label: 'Two pages' },
] as const satisfies readonly { value: MobileNavMode, label: string }[]

export const bubbleMotions = [
  { value: 'baseline', label: 'Current' },
  { value: 'spring', label: 'Spring' },
] as const

export type MobileDirection = typeof mobileDirections[number]['value']
export type PressFeel = typeof pressFeels[number]['value']
export type BubbleMotion = typeof bubbleMotions[number]['value']

const DIRECTION_KEY = 'memoh:mobile-direction'
const FEEL_KEY = 'memoh:mobile-feel'
const NAV_KEY = 'memoh:mobile-nav'
const BUBBLE_KEY = 'memoh:mobile-bubble'

function isAllowed<T extends string>(value: string | null, allowed: readonly { value: T }[]): value is T {
  return allowed.some(option => option.value === value)
}

function readStored<T extends string>(key: string, allowed: readonly { value: T }[], fallback: T): T {
  const stored = localStorage.getItem(key)
  return isAllowed(stored, allowed) ? stored : fallback
}

export const direction = ref<MobileDirection>(readStored(DIRECTION_KEY, mobileDirections, 'baseline'))
export const feel = ref<PressFeel>(readStored(FEEL_KEY, pressFeels, 'baseline'))
export const navForm = ref<MobileNavMode>(readStored(NAV_KEY, navForms, 'sheet'))
export const bubbleMotion = ref<BubbleMotion>(readStored(BUBBLE_KEY, bubbleMotions, 'baseline'))

// `search` is the query string the page was opened with (see main.ts).
export function syncChoices(search: string) {
  const params = new URLSearchParams(search)
  const fromUrl = <T extends string>(param: string, allowed: readonly { value: T }[], target: Ref<T>) => {
    const value = params.get(param)
    if (isAllowed(value, allowed)) target.value = value
  }
  fromUrl('direction', mobileDirections, direction)
  fromUrl('feel', pressFeels, feel)
  fromUrl('nav', navForms, navForm)
  fromUrl('bubble', bubbleMotions, bubbleMotion)

  const root = document.documentElement
  watch(direction, (value) => {
    localStorage.setItem(DIRECTION_KEY, value)
    if (value === 'baseline') {
      delete root.dataset.mobilePalette
      delete root.dataset.mobilePlatform
      return
    }
    root.dataset.mobilePalette = DIRECTION_PRESETS[value].palette
    root.dataset.mobilePlatform = DIRECTION_PRESETS[value].platform
  }, { immediate: true })
  watch(feel, (value) => {
    localStorage.setItem(FEEL_KEY, value)
    if (value === 'baseline') delete root.dataset.mobileFeel
    else root.dataset.mobileFeel = value
  }, { immediate: true })
  watch(navForm, (value) => {
    localStorage.setItem(NAV_KEY, value)
    mobileNavMode.value = value
  }, { immediate: true })
  watch(bubbleMotion, (value) => {
    localStorage.setItem(BUBBLE_KEY, value)
    bubbleSpringEnabled.value = value === 'spring'
  }, { immediate: true })
}
