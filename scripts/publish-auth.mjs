export function detectPublishAuthMode(env = process.env) {
  const hasOidcRequest = Boolean(
    env.ACTIONS_ID_TOKEN_REQUEST_URL?.trim()
      && env.ACTIONS_ID_TOKEN_REQUEST_TOKEN?.trim(),
  )

  // actions/setup-node exports a non-secret NODE_AUTH_TOKEN placeholder when
  // registry-url is configured. Prefer the complete GitHub OIDC environment so
  // that placeholder cannot send trusted publishing through token preflight.
  if (hasOidcRequest) {
    return 'oidc'
  }

  if (env.NODE_AUTH_TOKEN?.trim()) {
    return 'token'
  }

  return 'none'
}
