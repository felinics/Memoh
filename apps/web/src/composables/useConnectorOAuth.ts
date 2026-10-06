import { getBotsByBotIdConnectorsByConnectionId, postBotsByBotIdConnectorsByConnectionIdReauth } from '@memohai/sdk'
import { isApiErrorCode, resolveApiErrorMessage } from '@/utils/api-error'

type Translate = (key: string, params?: Record<string, unknown>) => string

const oauthTimeoutMs = 120_000

const oauthCancelledCode = 'oauth_cancelled'

// The OAuth helpers below throw bare internal codes; map them to i18n keys at
// the toast site so Error.message is never surfaced verbatim to the user.
export function connectorOAuthErrorKey(error: unknown): string | null {
  if (!(error instanceof Error)) return null
  if (error.message === 'oauth_popup_blocked') return 'connectors.oauthPopupBlocked'
  if (error.message === 'oauth_failed') return 'connectors.oauthFailed'
  return null
}

// Connect-It without an OAuth App for the connector is fixed by whoever runs
// it, so the copy tells the current user who that is.
export function connectorErrorMessage(
  error: unknown,
  t: Translate,
  context: { role: string, connector: string, fallback: string },
): string {
  const oauthKey = connectorOAuthErrorKey(error)
  if (oauthKey) return t(oauthKey)
  if (isApiErrorCode(error, 'connector.oauth_client_not_configured')) {
    const hint = context.role === 'admin'
      ? 'connectors.oauthAppNotConfigured.admin'
      : 'connectors.oauthAppNotConfigured.member'
    return t(hint, { connector: context.connector })
  }
  return resolveApiErrorMessage(error, context.fallback)
}

// A caller-initiated abort is not a failure: the caller swallows it instead of
// toasting, so it needs to be distinguishable from a real 'oauth_failed'.
export function isConnectorOAuthCancelled(error: unknown): boolean {
  return error instanceof Error && error.message === oauthCancelledCode
}

export function prepareConnectorOAuthPopup(loadingMessage: string): Window | null {
  if (window.api?.desktop?.openExternalUrl) return null
  const popup = window.open('', 'connect-it-oauth', 'width=600,height=700')
  if (!popup) return null

  popup.document.title = 'Connect-It'
  popup.document.body.style.cssText = [
    'margin:0',
    'min-height:100vh',
    'display:flex',
    'align-items:center',
    'justify-content:center',
    'font-family:system-ui,sans-serif',
    'color-scheme:light dark',
    'color:CanvasText',
    'background:Canvas',
  ].join(';')
  const message = popup.document.createElement('p')
  message.textContent = loadingMessage
  popup.document.body.replaceChildren(message)
  return popup
}

export async function openConnectorOAuthURL(url: string, popup: Window | null): Promise<void> {
  const desktopOpenExternal = window.api?.desktop?.openExternalUrl
  if (desktopOpenExternal) {
    await desktopOpenExternal(url)
    return
  }
  if (!popup || popup.closed) {
    throw new Error('oauth_popup_blocked')
  }
  popup.location.href = url
}

export function waitForConnectorOAuth(
  botId: string,
  connectionId: string,
  popup: Window | null,
  // Without a signal the wait can only end by success, by the popup closing,
  // or by the 2-minute timeout — none of which the user can trigger from the
  // page they came back to. The signal is how Cancel/Esc/overlay-click end it.
  signal?: AbortSignal,
): Promise<void> {
  return new Promise((resolve, reject) => {
    let completed = false
    const startedAt = Date.now()
    let pollTimer: ReturnType<typeof setTimeout> | undefined

    const finish = (error?: string) => {
      if (completed) return
      completed = true
      if (pollTimer) clearTimeout(pollTimer)
      signal?.removeEventListener('abort', onAbort)
      popup?.close()
      if (error) reject(new Error(error))
      else resolve()
    }

    // Hoisted so finish() above can unsubscribe it.
    function onAbort() {
      finish(oauthCancelledCode)
    }

    const poll = async () => {
      if (completed) return
      try {
        const { data: connector } = await getBotsByBotIdConnectorsByConnectionId({
          path: { bot_id: botId, connection_id: connectionId },
          throwOnError: true,
        })
        if (completed) return
        if (connector?.status === 'active') {
          finish()
          return
        }
        if (
          connector?.status === 'reauth_required'
          || connector?.status === 'authorization_failed'
        ) {
          finish('oauth_failed')
          return
        }
      } catch {
        // A transient status request failure is retried until the flow times out.
      }
      if ((popup && popup.closed) || Date.now() - startedAt > oauthTimeoutMs) {
        finish('oauth_failed')
        return
      }
      pollTimer = setTimeout(() => void poll(), 2000)
    }

    if (signal?.aborted) {
      finish(oauthCancelledCode)
      return
    }
    signal?.addEventListener('abort', onAbort)
    void poll()
  })
}

export async function reauthorizeConnector(botId: string, connectionId: string, popup: Window | null): Promise<void> {
  const { data } = await postBotsByBotIdConnectorsByConnectionIdReauth({
    path: { bot_id: botId, connection_id: connectionId },
    throwOnError: true,
  })
  if (!data.authorization_url) throw new Error('oauth_failed')
  await openConnectorOAuthURL(data.authorization_url, popup)
  await waitForConnectorOAuth(botId, connectionId, popup)
}

// The missing-OAuth-App copy is several steps long, so its notice stays until
// the reader dismisses it instead of timing out mid-read.
export function reauthorizeFailureNotice(
  error: unknown,
  t: Translate,
  role: string,
  connector: { type?: string },
  catalog: ReadonlyMap<string, { name?: string }>,
): { message: string, duration?: number } {
  const type = connector.type ?? ''
  const message = connectorErrorMessage(error, t, {
    role,
    connector: catalog.get(type)?.name || type,
    fallback: t('connectors.oauthFailed'),
  })
  const persistent = isApiErrorCode(error, 'connector.oauth_client_not_configured')
  return { message, duration: persistent ? Number.POSITIVE_INFINITY : undefined }
}
