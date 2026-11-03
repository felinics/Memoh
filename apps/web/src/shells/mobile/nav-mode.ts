import { ref } from 'vue'

// How the mobile shell hosts its main navigation:
//   sheet — a left Sheet over the chat with a scrim (the shipped behavior).
//   pager — the navigation and the chat are two full-width peer pages side by
//           side; opening the navigation slides both, nothing dims.
// Production always uses the sheet. Only the dev-only mobile direction
// comparison (pages/dev/mobile-directions) switches this, so both forms can be
// tried on a phone before one is chosen.
export type MobileNavMode = 'sheet' | 'pager'

export const mobileNavMode = ref<MobileNavMode>('sheet')
