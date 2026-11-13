const EXTENSION_LANGUAGE_MAP: Record<string, string> = {
  js: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  jsx: 'javascript',
  ts: 'typescript',
  tsx: 'typescript',
  mts: 'typescript',
  cts: 'typescript',
  vue: 'html',
  html: 'html',
  htm: 'html',
  css: 'css',
  scss: 'scss',
  less: 'less',
  json: 'json',
  jsonc: 'json',
  md: 'markdown',
  markdown: 'markdown',
  xml: 'xml',
  svg: 'xml',
  yaml: 'yaml',
  yml: 'yaml',
  toml: 'ini',
  ini: 'ini',
  conf: 'ini',
  sh: 'shell',
  bash: 'shell',
  zsh: 'shell',
  fish: 'shell',
  py: 'python',
  rb: 'ruby',
  go: 'go',
  rs: 'rust',
  java: 'java',
  kt: 'kotlin',
  kts: 'kotlin',
  swift: 'swift',
  c: 'c',
  cpp: 'cpp',
  cc: 'cpp',
  cxx: 'cpp',
  h: 'c',
  hpp: 'cpp',
  cs: 'csharp',
  php: 'php',
  r: 'r',
  R: 'r',
  sql: 'sql',
  graphql: 'graphql',
  gql: 'graphql',
  dockerfile: 'dockerfile',
  makefile: 'makefile',
  lua: 'lua',
  perl: 'perl',
  pl: 'perl',
  scala: 'scala',
  groovy: 'groovy',
  dart: 'dart',
  elixir: 'elixir',
  ex: 'elixir',
  exs: 'elixir',
  clj: 'clojure',
  bat: 'bat',
  cmd: 'bat',
  ps1: 'powershell',
  psm1: 'powershell',
  tf: 'hcl',
  hcl: 'hcl',
}

const TEXT_EXTENSIONS = new Set([
  ...Object.keys(EXTENSION_LANGUAGE_MAP),
  'txt', 'log', 'env', 'gitignore', 'gitattributes',
  'editorconfig', 'prettierrc', 'eslintrc', 'babelrc',
  'npmrc', 'nvmrc', 'dockerignore', 'lock', 'csv', 'tsv',
  'properties', 'cfg', 'cmake',
])

const TEXT_FILENAMES = new Set([
  'Makefile', 'Dockerfile', 'Vagrantfile', 'Gemfile',
  'Rakefile', 'Procfile', 'LICENSE', 'CHANGELOG',
  'README', 'AUTHORS', 'CONTRIBUTORS', '.gitignore',
  '.gitattributes', '.editorconfig', '.env', '.env.local',
  '.env.development', '.env.production', '.prettierrc',
  '.eslintrc', '.babelrc', '.npmrc', '.nvmrc',
  '.dockerignore',
])

const IMAGE_EXTENSIONS = new Set([
  'png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'ico', 'bmp', 'avif',
])

export function sortDirsFirst<T extends { isDir?: boolean, name?: string }>(entries: T[]): T[] {
  const byName = (a: T, b: T) => (a.name ?? '').localeCompare(b.name ?? '')
  const dirs = entries.filter(e => e.isDir).sort(byName)
  const files = entries.filter(e => !e.isDir).sort(byName)
  return [...dirs, ...files]
}

export function directoryEntriesFingerprint(
  entries: ReadonlyArray<{
    path?: string
    name?: string
    isDir?: boolean
    size?: number
    modTime?: string
    mode?: string
  }>,
): string {
  return entries
    .map(entry => [
      entry.path ?? '',
      entry.name ?? '',
      entry.isDir ? '1' : '0',
      entry.size ?? -1,
      entry.modTime ?? '',
      entry.mode ?? '',
    ].join('\u0000'))
    .join('\n')
}

export function getExtension(filename: string): string {
  const dotIndex = filename.lastIndexOf('.')
  if (dotIndex === -1 || dotIndex === filename.length - 1) return ''
  return filename.slice(dotIndex + 1).toLowerCase()
}

export function getLanguageByFilename(filename: string): string {
  const lower = filename.toLowerCase()
  if (lower === 'dockerfile' || lower.startsWith('dockerfile.')) return 'dockerfile'
  if (lower === 'makefile') return 'makefile'
  const ext = getExtension(filename)
  return EXTENSION_LANGUAGE_MAP[ext] ?? 'plaintext'
}

export function isTextFile(filename: string): boolean {
  if (TEXT_FILENAMES.has(filename)) return true
  const ext = getExtension(filename)
  if (!ext) return false
  return TEXT_EXTENSIONS.has(ext)
}

export function isImageFile(filename: string): boolean {
  const ext = getExtension(filename)
  return IMAGE_EXTENSIONS.has(ext)
}

export function isMarkdownFile(filename: string): boolean {
  const ext = getExtension(filename)
  return ext === 'md' || ext === 'markdown'
}

export function isHtmlFile(filename: string): boolean {
  const ext = getExtension(filename)
  return ext === 'html' || ext === 'htm'
}

export function isArchiveFile(filename: string | undefined): boolean {
  const lower = (filename ?? '').toLowerCase()
  return lower.endsWith('.zip') || lower.endsWith('.tar.gz') || lower.endsWith('.tgz')
}

export function formatFileSize(bytes: number | undefined): string {
  if (bytes === undefined || bytes === null) return ''
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  const size = bytes / Math.pow(1024, i)
  return `${size.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export function formatRelativeTime(dateStr: string | undefined): string {
  if (!dateStr) return ''
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffSec = Math.floor(diffMs / 1000)
  const diffMin = Math.floor(diffSec / 60)
  const diffHour = Math.floor(diffMin / 60)
  const diffDay = Math.floor(diffHour / 24)

  if (diffDay > 30) {
    return date.toLocaleDateString()
  }
  if (diffDay > 0) return `${diffDay}d ago`
  if (diffHour > 0) return `${diffHour}h ago`
  if (diffMin > 0) return `${diffMin}m ago`
  return 'just now'
}

export function joinPath(...parts: string[]): string {
  return parts
    .join('/')
    .replace(/\/+/g, '/')
    .replace(/\/$/, '') || '/'
}

export function parentPath(path: string): string {
  if (path === '/' || path === '') return '/'
  const parts = path.replace(/\/$/, '').split('/')
  parts.pop()
  return parts.join('/') || '/'
}

export function pathSegments(path: string): { name: string; path: string }[] {
  const parts = path.split('/').filter(Boolean)
  const segments: { name: string; path: string }[] = [{ name: '/', path: '/' }]
  let current = ''
  for (const part of parts) {
    current += '/' + part
    segments.push({ name: part, path: current })
  }
  return segments
}
