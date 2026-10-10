/**
 * Data contract for usage examples ("starter prompts").
 *
 * The shape is deliberately API-ready: text is stored as a locale map rather
 * than i18n keys, so the built-in catalog can later be replaced by a payload
 * from the backend without touching the UI.
 */

/** Capabilities an example may depend on. Keep in sync with capabilitySnapshotFrom. */
export type BotCapability = 'web_search' | 'memory' | 'email' | 'workspace' | 'browser' | 'schedule' | 'channel'

export type ChatExampleCategory = 'automation' | 'research' | 'writing' | 'dev' | 'memory' | 'team'

/** Where an example may be offered; a surface only shows examples tagged for it. */
export type ChatExampleSurface = 'welcome' | 'gallery' | 'onboarding'

/** Locale map shaped like a future API payload; `en` is the required fallback. */
export interface LocalizedText {
  en: string
  zh?: string
  ja?: string
}

export interface ChatExample {
  id: string
  category: ChatExampleCategory
  /** Key into CHAT_EXAMPLE_ICONS; unknown keys render the fallback icon. */
  icon: string
  /** Short label used on compact cards. */
  title: LocalizedText
  /** Full text written into the composer. */
  prompt: LocalizedText
  requires: BotCapability[]
  surfaces: ChatExampleSurface[]
  /** Higher sorts first within a surface. */
  priority: number
}

/** true = available, false = known missing, absent = unknown (never treated as missing). */
export type BotCapabilitySnapshot = Partial<Record<BotCapability, boolean>>
