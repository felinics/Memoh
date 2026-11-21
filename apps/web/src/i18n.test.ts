import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en.json'
import enRaw from '@/i18n/locales/en.json?raw'
import ja from '@/i18n/locales/ja.json'
import jaRaw from '@/i18n/locales/ja.json?raw'
import zh from '@/i18n/locales/zh.json'
import zhRaw from '@/i18n/locales/zh.json?raw'

interface LocaleMessage {
  path: string
  value: string
}

interface DuplicateJsonKey {
  path: string
  firstLine: number
  duplicateLine: number
}

const localeMessages = { en, ja, zh }
const rawLocaleSources = { en: enRaw, ja: jaRaw, zh: zhRaw }

function findDuplicateJsonKeys(source: string): DuplicateJsonKey[] {
  const duplicates: DuplicateJsonKey[] = []
  let index = 0
  let line = 1

  const advance = () => {
    if (source[index] === '\n') line += 1
    index += 1
  }

  const skipWhitespace = () => {
    while (index < source.length) {
      const ch = source[index]
      if (ch === ' ' || ch === '\t' || ch === '\r' || ch === '\n') {
        advance()
      } else {
        break
      }
    }
  }

  const expectChar = (expected: string) => {
    skipWhitespace()
    if (source[index] !== expected) {
      throw new SyntaxError(`Expected '${expected}' at line ${line}`)
    }
    advance()
  }

  const parseString = (): string => {
    expectChar('"')
    let out = ''
    while (index < source.length) {
      const ch = source[index]
      if (ch === '"') {
        advance()
        return out
      }
      if (ch === '\\') {
        advance()
        const esc = source[index]
        if (esc === undefined) break
        advance()
        if (esc === '"' || esc === '\\' || esc === '/') out += esc
        else if (esc === 'b') out += '\b'
        else if (esc === 'f') out += '\f'
        else if (esc === 'n') out += '\n'
        else if (esc === 'r') out += '\r'
        else if (esc === 't') out += '\t'
        else if (esc === 'u') {
          const hex = source.slice(index, index + 4)
          if (!/^[0-9a-fA-F]{4}$/.test(hex)) {
            throw new SyntaxError(`Invalid unicode escape at line ${line}`)
          }
          index += 4
          out += String.fromCharCode(Number.parseInt(hex, 16))
        } else {
          throw new SyntaxError(`Invalid escape '\\${esc}' at line ${line}`)
        }
        continue
      }
      out += ch
      advance()
    }
    throw new SyntaxError(`Unterminated string at line ${line}`)
  }

  const parseValue = (path: string[]) => {
    skipWhitespace()
    const ch = source[index]
    if (ch === '{') {
      parseObject(path)
      return
    }
    if (ch === '[') {
      parseArray(path)
      return
    }
    if (ch === '"') {
      parseString()
      return
    }
    const start = index
    while (index < source.length) {
      const cur = source[index]
      if (cur === undefined || cur === ',' || cur === '}' || cur === ']' || cur === ' ' || cur === '\t' || cur === '\r' || cur === '\n') {
        break
      }
      advance()
    }
    if (index === start) {
      throw new SyntaxError(`Unexpected token at line ${line}`)
    }
  }

  const parseArray = (path: string[]) => {
    expectChar('[')
    skipWhitespace()
    if (source[index] === ']') {
      advance()
      return
    }
    let itemIndex = 0
    while (index < source.length) {
      parseValue([...path, String(itemIndex)])
      itemIndex += 1
      skipWhitespace()
      if (source[index] === ',') {
        advance()
        continue
      }
      expectChar(']')
      return
    }
    throw new SyntaxError(`Unterminated array at line ${line}`)
  }

  const parseObject = (path: string[]) => {
    expectChar('{')
    skipWhitespace()
    if (source[index] === '}') {
      advance()
      return
    }
    const seen = new Map<string, number>()
    while (index < source.length) {
      skipWhitespace()
      const keyLine = line
      const key = parseString()
      const keyPath = [...path, key]
      const firstLine = seen.get(key)
      if (firstLine !== undefined) {
        duplicates.push({
          path: keyPath.join('.'),
          firstLine,
          duplicateLine: keyLine,
        })
      } else {
        seen.set(key, keyLine)
      }
      expectChar(':')
      parseValue(keyPath)
      skipWhitespace()
      if (source[index] === ',') {
        advance()
        continue
      }
      expectChar('}')
      return
    }
    throw new SyntaxError(`Unterminated object at line ${line}`)
  }

  parseValue([])
  skipWhitespace()
  if (index !== source.length) {
    throw new SyntaxError(`Trailing content at line ${line}`)
  }
  return duplicates
}

function collectLocaleMessages(value: unknown, path: string[] = []): LocaleMessage[] {
  if (typeof value === 'string') {
    return [{ path: path.join('.'), value }]
  }
  if (Array.isArray(value)) {
    return value.flatMap((item, index) => collectLocaleMessages(item, [...path, String(index)]))
  }
  if (value && typeof value === 'object') {
    return Object.entries(value).flatMap(([key, item]) => collectLocaleMessages(item, [...path, key]))
  }
  return []
}

describe('i18n locale messages', () => {
  for (const [locale, rawSource] of Object.entries(rawLocaleSources)) {
    it(`${locale} has no duplicate keys within the same JSON object`, () => {
      expect(
        findDuplicateJsonKeys(rawSource),
        `duplicate same-object JSON keys in ${locale}.json`,
      ).toEqual([])
    })
  }

  // A key missing from one language renders its fallback (or the raw key) only in
  // that language, so nothing else catches it; keys left behind after a feature is
  // removed ship as dead weight in every bundle. Every locale must carry exactly
  // the keys of en.
  const enPaths = new Set(collectLocaleMessages(en).map(({ path }) => path))
  for (const [locale, messages] of Object.entries(localeMessages)) {
    if (locale === 'en') continue
    it(`${locale} has the same keys as en`, () => {
      const paths = new Set(collectLocaleMessages(messages).map(({ path }) => path))
      expect([...enPaths].filter(path => !paths.has(path)), `keys missing from ${locale}`).toEqual([])
      expect([...paths].filter(path => !enPaths.has(path)), `keys only in ${locale}`).toEqual([])
    })
  }

  for (const [locale, messages] of Object.entries(localeMessages)) {
    it(`${locale} messages compile`, () => {
      const entries = collectLocaleMessages(messages)
      const flattened = Object.fromEntries(
        entries.map(({ value }, index) => [`message_${index}`, value]),
      )
      const i18n = createI18n({
        legacy: false,
        locale,
        fallbackLocale: false,
        missingWarn: false,
        fallbackWarn: false,
        warnHtmlMessage: false,
        messages: { [locale]: flattened },
      })

      for (const [index, entry] of entries.entries()) {
        expect(
          () => i18n.global.t(`message_${index}`),
          `${locale}.${entry.path} should compile`,
        ).not.toThrow()
      }
    })
  }
})
