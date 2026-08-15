import { describe, expect, it } from 'vitest'
import {
  gptImage2DimensionBounds,
  imageBillingTierForSize,
  normalizeGPTImage2Dimension,
  validateGPTImage2Size
} from '../gptImage2'

describe('validateGPTImage2Size', () => {
  it('accepts auto and valid boundary resolutions', () => {
    expect(validateGPTImage2Size('auto')).toMatchObject({ valid: true, auto: true })
    expect(validateGPTImage2Size('1024x640')).toMatchObject({ valid: true, pixels: 655_360 })
    expect(validateGPTImage2Size('3840x2160')).toMatchObject({ valid: true, pixels: 8_294_400, experimental: true })
    expect(validateGPTImage2Size('1152x2048')).toMatchObject({ valid: true, experimental: false })
  })

  it.each([
    ['1024-1024', 'format'],
    ['1025x1024', 'multiple'],
    ['3856x1024', 'edge'],
    ['3840x1264', 'ratio'],
    ['800x800', 'pixelsMin'],
    ['3840x2176', 'pixelsMax']
  ])('rejects %s with %s', (size, error) => {
    expect(validateGPTImage2Size(size)).toMatchObject({ valid: false, error })
  })

  it('maps arbitrary valid dimensions to the existing billing tiers', () => {
    expect(imageBillingTierForSize('auto')).toBe('2K')
    expect(imageBillingTierForSize('1024x640')).toBe('1K')
    expect(imageBillingTierForSize('1536x864')).toBe('2K')
    expect(imageBillingTierForSize('2560x1440')).toBe('4K')
  })

  it('derives safe ranges from the opposite edge', () => {
    expect(gptImage2DimensionBounds(1024)).toEqual({ min: 640, max: 3072 })
    expect(gptImage2DimensionBounds(3840)).toEqual({ min: 1280, max: 2160 })
    expect(gptImage2DimensionBounds(16)).toBeNull()
  })

  it('snaps typed dimensions to the nearest valid value', () => {
    expect(normalizeGPTImage2Dimension(1537, 1024)).toBe(1536)
    expect(normalizeGPTImage2Dimension(4000, 1024)).toBe(3072)
    expect(normalizeGPTImage2Dimension(128, 1024)).toBe(640)
  })
})
