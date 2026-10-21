// File-type icons for the explorer tree, rendered from the vendored Seti icon
// font + its official association map (see ./seti/ATTRIBUTION.md). We do NOT
// hand-maintain a filename→icon table: the glyph + color come straight from
// Seti's own theme JSON, so the tree reproduces the editor's file icons exactly.
//
// Matching order mirrors the editor's icon resolver:
//   1. exact file name        (readme.md, license, docker-compose.yml, ...)
//   2. file extension, longest suffix first (foo.test.ts → "test.ts" then "ts")
//   3. language id             (md → markdown, ts → typescript, ...)
//   4. the default file glyph
// The only thing we supply is the small, deterministic extension/name → language
// table the editor would otherwise derive from its language contributions.
import theme from './seti/vs-seti-icon-theme.json'
import './seti/seti.css'

interface IconDef { fontCharacter: string, fontColor?: string }
interface IconMaps {
  file: string
  fileExtensions: Record<string, string>
  fileNames: Record<string, string>
  languageIds: Record<string, string>
}
interface SetiTheme extends IconMaps {
  iconDefinitions: Record<string, IconDef>
  light: Partial<IconMaps>
}

const seti = theme as unknown as SetiTheme

/** The Seti font-family id; the glyph spans are styled with this in CSS. */
export const SETI_FONT_FAMILY = 'seti'

export interface FileIcon {
  /** The glyph to render (a single Private-Use-Area code point). */
  char: string
  /** The glyph color for the active theme. */
  color: string
}

// Extension → Seti language id. Covers the languages whose icon Seti exposes
// only via `languageIds` (a bare `.ts`/`.md`/`.go` has no `fileExtensions`
// entry). Extensions already present in Seti's `fileExtensions` (vue, svelte,
// toml, tf, graphql, images, fonts, ...) are matched in step 2 and need no row
// here.
const EXTENSION_LANGUAGE: Record<string, string> = {
  md: 'markdown', markdown: 'markdown', mdown: 'markdown', mkd: 'markdown', mdx: 'markdown',
  ts: 'typescript', mts: 'typescript', cts: 'typescript',
  tsx: 'typescriptreact',
  js: 'javascript', mjs: 'javascript', cjs: 'javascript',
  jsx: 'javascriptreact',
  json: 'json', json5: 'json', jsonc: 'jsonc', jsonl: 'jsonl', ndjson: 'jsonl',
  yaml: 'yaml', yml: 'yaml',
  xml: 'xml', xaml: 'xml', xhtml: 'html', html: 'html', htm: 'html',
  go: 'go',
  py: 'python', pyw: 'python', pyi: 'python',
  rb: 'ruby', rake: 'ruby', gemspec: 'ruby',
  rs: 'rust', java: 'java',
  c: 'c', cpp: 'cpp', cc: 'cpp', cxx: 'cpp', 'c++': 'cpp',
  cs: 'csharp', css: 'css', scss: 'scss', less: 'less',
  php: 'php', swift: 'swift', lua: 'lua', sql: 'sql',
  ini: 'properties', cfg: 'properties', conf: 'properties', properties: 'properties',
  dart: 'dart', jl: 'julia',
  sh: 'shellscript', bash: 'shellscript', zsh: 'shellscript', fish: 'shellscript', ksh: 'shellscript',
  ps1: 'powershell', psm1: 'powershell', psd1: 'powershell',
  bat: 'bat', cmd: 'bat',
  clj: 'clojure', cljs: 'clojure', cljc: 'clojure',
  coffee: 'coffeescript',
  fs: 'fsharp', fsi: 'fsharp', fsx: 'fsharp',
  m: 'objective-c', mm: 'objective-cpp',
  tex: 'tex', latex: 'latex', sty: 'tex',
  kt: 'kotlin', kts: 'kotlin',
  groovy: 'groovy', gvy: 'groovy', gradle: 'gradle',
  vue: 'vue', svelte: 'svelte', vala: 'vala',
  hbs: 'handlebars', handlebars: 'handlebars',
  jade: 'jade', pug: 'jade',
  ex: 'elixir', exs: 'elixir', elm: 'elm',
  hs: 'haskell', lhs: 'haskell',
  bicep: 'bicep', razor: 'razor', cshtml: 'razor',
}

// Exact file name → language id for files the editor types by name rather than
// extension (Dockerfile, Makefile, compose files, ignore files).
const FILENAME_LANGUAGE: Record<string, string> = {
  'dockerfile': 'dockerfile', 'containerfile': 'dockerfile',
  'makefile': 'makefile', 'gnumakefile': 'makefile',
  'docker-compose.yml': 'dockercompose', 'docker-compose.yaml': 'dockercompose',
  'compose.yml': 'dockercompose', 'compose.yaml': 'dockercompose',
}

function* extensionCandidates(name: string): Generator<string> {
  const parts = name.split('.')
  for (let i = 1; i < parts.length; i++) {
    yield parts.slice(i).join('.')
  }
}

function languageFor(name: string): string | undefined {
  if (FILENAME_LANGUAGE[name]) return FILENAME_LANGUAGE[name]
  if (name === 'dockerfile' || name.startsWith('dockerfile.')) return 'dockerfile'
  if (name === '.env' || name.startsWith('.env.')) return 'dotenv'
  const ext = name.split('.').pop()
  return ext === undefined ? undefined : EXTENSION_LANGUAGE[ext]
}

function iconKey(name: string, light: Partial<IconMaps> | undefined): string {
  if (light?.fileNames?.[name]) return light.fileNames[name]
  if (seti.fileNames[name]) return seti.fileNames[name]

  for (const candidate of extensionCandidates(name)) {
    if (light?.fileExtensions?.[candidate]) return light.fileExtensions[candidate]
    if (seti.fileExtensions[candidate]) return seti.fileExtensions[candidate]
  }

  const language = languageFor(name)
  if (language) {
    if (light?.languageIds?.[language]) return light.languageIds[language]
    if (seti.languageIds[language]) return seti.languageIds[language]
  }

  return light?.file ?? seti.file
}

function glyph(fontCharacter: string): string {
  const code = parseInt(fontCharacter.replace(/^\\+/, ''), 16)
  return Number.isNaN(code) ? '' : String.fromCodePoint(code)
}

/**
 * Resolve a file name to its Seti glyph + color. `isDark` selects the dark
 * palette; light mode uses Seti's darker `*_light` variants for contrast on a
 * light surface.
 */
export function resolveFileIcon(filename: string, isDark: boolean): FileIcon {
  const name = (filename ?? '').toLowerCase()
  const key = iconKey(name, isDark ? undefined : seti.light)
  // The vendored theme always defines its default `file` icon.
  const def = seti.iconDefinitions[key] ?? seti.iconDefinitions[seti.file]!
  return { char: glyph(def.fontCharacter), color: def.fontColor ?? 'currentColor' }
}
