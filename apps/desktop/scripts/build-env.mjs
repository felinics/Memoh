const VERSION_PATTERN = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/
const APP_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9.-]*$/

export function applyEnvFile(contents, env = process.env) {
  for (const rawLine of contents.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || line.startsWith('#')) continue

    const body = line.startsWith('export ') ? line.slice('export '.length).trim() : line
    const separator = body.indexOf('=')
    if (separator === -1) continue

    const key = body.slice(0, separator).trim()
    if (!key || key in env) continue

    let value = body.slice(separator + 1).trim()
    if (
      (value.startsWith('"') && value.endsWith('"'))
      || (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value.slice(1, -1)
    }
    env[key] = value
  }
  return env
}

export function isEnabled(value) {
  return /^(1|true|yes|on)$/i.test(value?.trim() ?? '')
}

export function normalizeDesktopVersion(value) {
  const version = value?.trim().replace(/^v/i, '') ?? ''
  if (!version) return ''
  if (!VERSION_PATTERN.test(version)) {
    throw new Error(`MEMOH_DESKTOP_VERSION must be a semantic version, got "${value}"`)
  }
  return version
}

export function normalizeDesktopAppId(value) {
  const appId = value?.trim() ?? ''
  if (!appId) return ''
  if (!APP_ID_PATTERN.test(appId) || !appId.includes('.')) {
    throw new Error(`MEMOH_DESKTOP_APP_ID must be a reverse-DNS identifier, got "${value}"`)
  }
  return appId
}

export function normalizeUpdateBaseUrl(value) {
  const rawUrl = value?.trim() ?? ''
  if (!rawUrl) return ''

  let url
  try {
    url = new URL(rawUrl)
  } catch {
    throw new Error(`MEMOH_DESKTOP_UPDATE_BASE_URL must be an absolute HTTP(S) URL, got "${value}"`)
  }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) {
    throw new Error(`MEMOH_DESKTOP_UPDATE_BASE_URL must be an absolute HTTP(S) URL without credentials, got "${value}"`)
  }
  return url.toString().replace(/\/+$/, '')
}

export function stripPublishArgs(args) {
  const result = []
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index]
    if (arg === '--publish' || arg === '-p') {
      index += 1
      continue
    }
    if (arg.startsWith('--publish=') || arg.startsWith('-p=')) continue
    result.push(arg)
  }
  return result
}

export function buildElectronBuilderArgs(env = process.env) {
  const args = []
  const version = normalizeDesktopVersion(env.MEMOH_DESKTOP_VERSION)
  const appId = normalizeDesktopAppId(env.MEMOH_DESKTOP_APP_ID)
  const updateBaseUrl = normalizeUpdateBaseUrl(env.MEMOH_DESKTOP_UPDATE_BASE_URL)

  if (version) args.push(`-c.extraMetadata.version=${version}`)
  if (appId) args.push(`-c.appId=${appId}`)
  if (updateBaseUrl) {
    args.push(
      '-c.publish.provider=generic',
      `-c.publish.url=${updateBaseUrl}`,
    )
  }

  // Build scripts own publication policy. electron-builder only creates local
  // artifacts and update metadata; a downstream release workflow uploads them.
  args.push('--publish', 'never')
  return args
}

export function missingRequiredMacSigningEnv(env = process.env) {
  const required = [
    ['APPLE_CERTIFICATE or CSC_LINK', env.APPLE_CERTIFICATE || env.CSC_LINK],
    ['APPLE_CERTIFICATE_PASSWORD or CSC_KEY_PASSWORD', env.APPLE_CERTIFICATE_PASSWORD || env.CSC_KEY_PASSWORD],
    ['APPLE_API_KEY', env.APPLE_API_KEY],
    ['APPLE_API_KEY_ID', env.APPLE_API_KEY_ID],
    ['APPLE_API_ISSUER', env.APPLE_API_ISSUER],
  ]
  return required.filter(([, value]) => !value?.trim()).map(([name]) => name)
}

export function hasMacNotarizationEnv(env = process.env) {
  return Boolean(
    (env.CSC_LINK || env.APPLE_CERTIFICATE)?.trim()
    && (env.CSC_KEY_PASSWORD || env.APPLE_CERTIFICATE_PASSWORD)?.trim()
    && env.APPLE_API_KEY?.trim()
    && env.APPLE_API_KEY_ID?.trim()
    && env.APPLE_API_ISSUER?.trim(),
  )
}
