import { describe, expect, it } from 'vitest'

import { calculateCacheHitRate, formatCacheHitRate } from '../cacheHitRate'

describe('cacheHitRate', () => {
  it('calculates cache reads against all prompt-side tokens', () => {
    expect(calculateCacheHitRate(500, 0, 1500)).toBe(75)
    expect(formatCacheHitRate(500, 0, 1500)).toBe('75.0%')
  })

  it('includes cache creation tokens in the denominator', () => {
    expect(calculateCacheHitRate(200, 300, 500)).toBe(50)
  })

  it('returns zero when there are no cache reads', () => {
    expect(calculateCacheHitRate(1000, 250, 0)).toBe(0)
  })

  it('returns zero when there are no prompt-side tokens', () => {
    expect(calculateCacheHitRate(0, 0, 0)).toBe(0)
    expect(formatCacheHitRate(0, 0, 0)).toBe('0.0%')
  })

  it('ignores negative and non-finite values', () => {
    expect(calculateCacheHitRate(-100, Number.NaN, 100)).toBe(100)
  })
})
