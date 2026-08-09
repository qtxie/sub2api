import { describe, expect, it } from 'vitest'
import { parseOpenAIAPIKeyListInput } from '@/utils/openaiApiKeyList'

describe('parseOpenAIAPIKeyListInput', () => {
  it('trims, deduplicates, and excludes the primary key', () => {
    expect(parseOpenAIAPIKeyListInput(' sk-fallback-1 \n\nsk-primary\nsk-fallback-1\nsk-fallback-2 ', 'sk-primary')).toEqual([
      'sk-fallback-1',
      'sk-fallback-2'
    ])
  })

  it('returns an empty list for blank input', () => {
    expect(parseOpenAIAPIKeyListInput(' \n\r\n ')).toEqual([])
  })
})
