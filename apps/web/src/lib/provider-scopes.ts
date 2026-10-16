// The provider-family settings scopes, presented as ONE page: the sidebar links
// only "Providers", and the four scopes are tabs inside it (the container is
// pages/providers/index.vue; the rail is components/provider-scope-tabs).
// Everything routes through /settings/providers?tab=<scope>; the old flat
// routes (/settings/web-search etc.) redirect here so existing links survive.
//
// This list is the single source for scope order + label + each scope's
// detail-view query key — the tab rail, the container (detail detection) and
// future callers must all derive from it so the strands can never drift apart.
// Order note: web-search is LAST on purpose — it is the one scope with no Add
// dialog (its list IS the catalog), so parking it at the end keeps the header
// Add button present for every tab until the final one.
export const PROVIDER_SCOPES = [
  { tab: 'models', labelKey: 'models.title', detailQueryKey: 'provider' },
  { tab: 'voice', labelKey: 'sidebar.voice', detailQueryKey: 'voiceProvider' },
  { tab: 'video', labelKey: 'sidebar.video', detailQueryKey: 'videoProvider' },
  { tab: 'web-search', labelKey: 'sidebar.webSearch', detailQueryKey: 'webProvider' },
] as const

export type ProviderScopeTab = typeof PROVIDER_SCOPES[number]['tab']

export function isProviderScopeTab(value: unknown): value is ProviderScopeTab {
  return typeof value === 'string'
    && (PROVIDER_SCOPES as readonly { tab: string }[]).some(scope => scope.tab === value)
}
