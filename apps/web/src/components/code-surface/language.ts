import { bundledLanguages } from 'shiki'

// Explicit fence ids that AI output uses but Shiki does not ship under that
// name. Values must be ids present in `bundledLanguages`; everything else falls
// back to plain text, which is what the previous highlighter did when a grammar
// failed to load.
const FENCE_LANGUAGE_ALIASES: Record<string, string> = {
  'c#': 'csharp',
  'c++': 'cpp',
  'cs': 'csharp',
  'console': 'shellscript',
  'env': 'dotenv',
  'golang': 'go',
  'gql': 'graphql',
  'htm': 'html',
  'js': 'javascript',
  'kt': 'kotlin',
  'md': 'markdown',
  'patch': 'diff',
  'plain': 'text',
  'plaintext': 'text',
  'ps1': 'powershell',
  'py': 'python',
  'rb': 'ruby',
  'rs': 'rust',
  'sh': 'shellscript',
  'shell': 'shellscript',
  'svg': 'xml',
  'ts': 'typescript',
  'txt': 'text',
  'yml': 'yaml',
  'zsh': 'shellscript',
}

const bundled = bundledLanguages as Record<string, unknown>

/**
 * Maps a fenced-code language id to a Shiki language the diff surface can load.
 * Returns `undefined` when the fence has no language (the surface then infers
 * from the file name), and `'text'` when the id is unknown so the block still
 * renders instead of failing the whole surface.
 */
export function resolveFenceLanguage(language?: string | null): string | undefined {
  const raw = (language ?? '').trim().toLowerCase().split(/\s+/, 1)[0] ?? ''
  if (!raw) return undefined
  const alias = FENCE_LANGUAGE_ALIASES[raw] ?? raw
  return Object.hasOwn(bundled, alias) ? alias : 'text'
}
