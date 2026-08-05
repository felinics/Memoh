import { getBotsByBotIdConnectorsByConnectionId } from '@memohai/sdk'

const oauthTimeoutMs = 120_000

// The OAuth helpers below throw bare internal codes; map them to i18n keys at
// the toast site so Error.message is never surfaced verbatim to the user.
export function connectorOAuthErrorKey(error: unknown): string | null {
  if (!(error instanceof Error)) return null
  if (error.message === 'oauth_popup_blocked') return 'connectors.oauthPopupBlocked'
  if (error.message === 'oauth_failed') return 'connectors.oauthFailed'
  return null
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
): Promise<void> {
  return new Promise((resolve, reject) => {
    let completed = false
    const startedAt = Date.now()
    let pollTimer: ReturnType<typeof setTimeout> | undefined

    const finish = (error?: string) => {
      if (completed) return
      completed = true
      if (pollTimer) clearTimeout(pollTimer)
      popup?.close()
      if (error) reject(new Error(error))
      else resolve()
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

    void poll()
  })
}
