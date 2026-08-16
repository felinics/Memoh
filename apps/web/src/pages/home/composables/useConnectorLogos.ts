import {
  computed,
  hasInjectionContext,
  inject,
  provide,
  type ComputedRef,
  type InjectionKey,
  type Ref,
} from 'vue'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdConnectors, getConnectorsCatalog } from '@memohai/sdk'
import { useCapabilitiesStore } from '@/store/capabilities'
import type { ChatViewTarget } from '@/store/chat-list'

// What a connector tool row needs to identify where the call came from.
export interface ConnectorIdentity {
  // The binding's durable tool namespace — also the map key, so a bot with two
  // bindings of the same connector type ("notion", "notion-2") stays distinct.
  alias: string
  // Catalog display name ("Notion"), used as the logo's tooltip.
  name: string
  // Catalog artwork URL; empty when the catalog carries no icon for the type.
  iconUrl: string
}

export type ConnectorLookup = (toolName: string) => ConnectorIdentity | null

const connectorLookupKey: InjectionKey<ComputedRef<ConnectorLookup>> = Symbol('connector-lookup')

// Connect-It namespaces every connector tool as `<alias>__<tool>` (the Go SDK's
// MCPSessionConfig documents "github" exposing "github__search_repositories"),
// so the alias is what a tool name carries — not the connector type. Splitting
// on the FIRST separator keeps tool names that contain their own "__" intact.
const ALIAS_SEPARATOR = '__'

export function connectorAliasOf(toolName: string): string {
  const index = toolName.indexOf(ALIAS_SEPARATOR)
  if (index <= 0) return ''
  return toolName.slice(0, index)
}

const emptyLookup: ConnectorLookup = () => null

/**
 * Resolve the current bot's connector bindings once per chat pane, so the tool
 * rows below can mark a call with its connector's logo. The two queries share
 * their keys with the bot Connectors page, so the pane usually renders from
 * cache (and from the persisted cache on a cold reload) rather than refetching.
 */
export function provideConnectorLogos(
  target: Ref<ChatViewTarget> | ComputedRef<ChatViewTarget>,
): ComputedRef<ConnectorLookup> {
  const capabilities = useCapabilitiesStore()
  void capabilities.load()

  const botId = computed(() => target.value.botId.trim())
  // Connectors are a server capability: without it these endpoints do not
  // answer, so nothing is asked of them.
  const enabled = () => capabilities.connectors && botId.value !== ''

  const catalogQuery = useQuery({
    key: ['connectors-catalog'],
    query: async () => {
      const { data } = await getConnectorsCatalog({ throwOnError: true })
      return data
    },
    enabled,
  })

  const bindingsQuery = useQuery({
    key: () => ['bot-connectors', botId.value],
    query: async () => {
      const { data } = await getBotsByBotIdConnectors({
        path: { bot_id: botId.value },
        throwOnError: true,
      })
      return data.items ?? []
    },
    enabled,
  })

  const byAlias = computed(() => {
    const catalog = new Map(
      (catalogQuery.data.value ?? []).map(item => [item.type ?? '', item]),
    )
    const out = new Map<string, ConnectorIdentity>()
    for (const binding of bindingsQuery.data.value ?? []) {
      const alias = binding.alias?.trim() ?? ''
      if (!alias) continue
      const type = binding.connector_type?.trim() ?? ''
      const metadata = type ? catalog.get(type) : undefined
      out.set(alias, {
        alias,
        name: metadata?.name?.trim() || type || alias,
        iconUrl: metadata?.icon_url?.trim() ?? '',
      })
    }
    return out
  })

  const lookup = computed<ConnectorLookup>(() => {
    const map = byAlias.value
    if (map.size === 0) return emptyLookup
    return (toolName: string) => {
      const alias = connectorAliasOf(toolName.trim())
      if (!alias) return null
      return map.get(alias) ?? null
    }
  })

  provide(connectorLookupKey, lookup)
  return lookup
}

/**
 * Tool rows rendered outside a chat pane (or before the bindings resolve) get a
 * lookup that finds nothing, so they simply draw no logo.
 */
export function useConnectorLogos(): ComputedRef<ConnectorLookup> {
  const provided = hasInjectionContext() ? inject(connectorLookupKey, null) : null
  return provided ?? computed(() => emptyLookup)
}
