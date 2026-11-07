import { loadLocaleMessages } from '@/i18n/messages'

// The running app downloads only its active language and mounts once it has
// loaded (see i18n/messages.ts). Tests render components and read copy in any
// of the three languages without that mount step, so load all of them before
// any test module runs; i18n.ts then registers them with vue-i18n on creation.
await Promise.all((['en', 'zh', 'ja'] as const).map(loadLocaleMessages))
