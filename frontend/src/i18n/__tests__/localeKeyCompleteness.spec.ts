import { describe, expect, it } from 'vitest'
import { createParser } from '@intlify/message-compiler'

import en from '../locales/en'
import zh from '../locales/zh'

type LocaleValue = Record<string, unknown>

function collectLeafMessages(value: unknown, prefix = ''): Array<[string, string]> {
  if (typeof value === 'string') {
    return prefix ? [[prefix, value]] : []
  }
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return []
  }
  return Object.entries(value as LocaleValue).flatMap(([key, child]) =>
    collectLeafMessages(child, prefix ? `${prefix}.${key}` : key)
  )
}

function flattenLeafKeys(value: unknown, prefix = ''): string[] {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return prefix ? [prefix] : []
  }

  return Object.entries(value as LocaleValue).flatMap(([key, child]) => {
    const path = prefix ? `${prefix}.${key}` : key
    return flattenLeafKeys(child, path)
  })
}

function collectStaticSourceKeys(source: string): string[] {
  const keys = new Set<string>()

  // Covers useI18n().t(), the global $t(), and direct i18n.t() calls in both
  // TypeScript and Vue script/template source. Dynamic suffixes are checked by
  // the runtime type of the value and cannot be proven from source text alone.
  const translationCalls = /(?:\bi18n\.t|\$t|\bt)\s*\(\s*(['"])([^'"\r\n]+)\1/g
  for (const match of source.matchAll(translationCalls)) {
    const key = match[2]
    if (!key.endsWith('.')) {
      keys.add(key)
    }
  }

  // Router metadata and i18n-t are key references without a t() call.
  const keyReferences = /(?:keypath|titleKey|descriptionKey)\s*[:=]\s*(['"])([^'"\r\n]+)\1/g
  for (const match of source.matchAll(keyReferences)) {
    keys.add(match[2])
  }

  const i18nTKeypaths = /<i18n-t\b[^>]*\bkeypath\s*=\s*(['"])([^'"\r\n]+)\1/gi
  for (const match of source.matchAll(i18nTKeypaths)) {
    keys.add(match[2])
  }

  return [...keys]
}

function sourceKeys(): string[] {
  const sourceFiles = import.meta.glob('../../**/*.{ts,vue}', {
    query: '?raw',
    import: 'default',
    eager: true
  }) as Record<string, string>

  return Object.entries(sourceFiles)
    .filter(([path]) => !path.includes('/__tests__/') && !/\.(spec|test)\.ts$/.test(path))
    .flatMap(([, source]) => collectStaticSourceKeys(source))
}

function missingKeys(usedKeys: string[], availableKeys: Set<string>): string[] {
  return usedKeys.filter((key) => !availableKeys.has(key)).sort()
}

describe('locale key completeness', () => {
  const enKeys = new Set(flattenLeafKeys(en))
  const zhKeys = new Set(flattenLeafKeys(zh))
  const usedKeys = [...new Set(sourceKeys())].sort()

  it('keeps English and Chinese locale schemas identical', () => {
    expect([...enKeys].filter((key) => !zhKeys.has(key)).sort()).toEqual([])
    expect([...zhKeys].filter((key) => !enKeys.has(key)).sort()).toEqual([])
  })

  it('contains a non-empty message for every locale leaf', () => {
    for (const [locale, messages] of Object.entries({ en, zh })) {
      const emptyKeys = flattenLeafKeys(messages).filter((key) => {
        let current: unknown = messages
        for (const segment of key.split('.')) {
          current = (current as LocaleValue)[segment]
        }
        return typeof current !== 'string' || current.trim() === ''
      })
      expect(emptyKeys, `${locale} has empty or non-string messages`).toEqual([])
    }
  })

  // A raw "{...}" inside a message is parsed by vue-i18n as an interpolation
  // placeholder; invalid ones throw SyntaxError during render and take down the
  // whole component subtree (e.g. the account edit dialog). Braces that should be
  // rendered literally must be escaped as {'{'} / {'}'}.
  it('keeps every locale message parseable by the vue-i18n message compiler', () => {
    const failures: string[] = []
    for (const [locale, messages] of Object.entries({ en, zh })) {
      for (const [key, message] of collectLeafMessages(messages)) {
        const parser = createParser({
          onError: (error) => failures.push(`${locale}.${key}: ${error.message}`)
        })
        parser.parse(message)
      }
    }
    expect(failures).toEqual([])
  })

  it('contains every statically referenced production key', () => {
    expect(missingKeys(usedKeys, enKeys), 'English locale is missing referenced keys').toEqual([])
    expect(missingKeys(usedKeys, zhKeys), 'Chinese locale is missing referenced keys').toEqual([])
  })
})
