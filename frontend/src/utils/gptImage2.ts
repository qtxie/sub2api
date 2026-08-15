export const GPT_IMAGE_2_MIN_PIXELS = 655_360
export const GPT_IMAGE_2_MAX_PIXELS = 8_294_400
export const GPT_IMAGE_2_MAX_EDGE = 3_840
export const GPT_IMAGE_2_EXPERIMENTAL_PIXELS = 3_686_400

export const GPT_IMAGE_2_SIZE_PRESETS = [
  '1024x1024',
  '1536x1024',
  '1024x1536',
  '2048x2048',
  '2048x1152',
  '1152x2048',
  '3840x2160',
  '2160x3840'
] as const

export type GPTImage2SizeError = 'format' | 'multiple' | 'edge' | 'ratio' | 'pixelsMin' | 'pixelsMax'

export interface GPTImage2SizeValidation {
  valid: boolean
  auto: boolean
  width?: number
  height?: number
  pixels?: number
  experimental: boolean
  error?: GPTImage2SizeError
}

export interface GPTImage2DimensionBounds {
  min: number
  max: number
}

export function validateGPTImage2Size(value: string): GPTImage2SizeValidation {
  const normalized = value.trim().toLowerCase()
  if (normalized === 'auto') return { valid: true, auto: true, experimental: false }

  const match = /^(\d+)x(\d+)$/.exec(normalized)
  if (!match) return invalidSize('format')

  const width = Number(match[1])
  const height = Number(match[2])
  if (!Number.isSafeInteger(width) || !Number.isSafeInteger(height) || width <= 0 || height <= 0) {
    return invalidSize('format')
  }
  if (width % 16 !== 0 || height % 16 !== 0) return invalidSize('multiple', width, height)
  if (width > GPT_IMAGE_2_MAX_EDGE || height > GPT_IMAGE_2_MAX_EDGE) return invalidSize('edge', width, height)

  const shortEdge = Math.min(width, height)
  const longEdge = Math.max(width, height)
  if (longEdge > shortEdge * 3) return invalidSize('ratio', width, height)

  const pixels = width * height
  if (pixels < GPT_IMAGE_2_MIN_PIXELS) return invalidSize('pixelsMin', width, height, pixels)
  if (pixels > GPT_IMAGE_2_MAX_PIXELS) return invalidSize('pixelsMax', width, height, pixels)

  return {
    valid: true,
    auto: false,
    width,
    height,
    pixels,
    experimental: pixels > GPT_IMAGE_2_EXPERIMENTAL_PIXELS
  }
}

export function imageBillingTierForSize(value: string): '1K' | '2K' | '4K' {
  const result = validateGPTImage2Size(value)
  if (!result.valid || result.auto || !result.width || !result.height) return '2K'
  const maxEdge = Math.max(result.width, result.height)
  if (maxEdge <= 1024) return '1K'
  if (maxEdge <= 2048) return '2K'
  return '4K'
}

export function snapGPTImage2Edge(value: number): number | null {
  if (!Number.isFinite(value) || value <= 0) return null
  const snapped = Math.round(value / 16) * 16
  return Math.min(GPT_IMAGE_2_MAX_EDGE, Math.max(16, snapped))
}

export function gptImage2DimensionBounds(otherEdge: number): GPTImage2DimensionBounds | null {
  const normalizedOther = snapGPTImage2Edge(otherEdge)
  if (!normalizedOther) return null

  const min = roundUpTo16(Math.max(16, normalizedOther / 3, GPT_IMAGE_2_MIN_PIXELS / normalizedOther))
  const max = roundDownTo16(Math.min(
    GPT_IMAGE_2_MAX_EDGE,
    normalizedOther * 3,
    GPT_IMAGE_2_MAX_PIXELS / normalizedOther
  ))
  return min <= max ? { min, max } : null
}

export function normalizeGPTImage2Dimension(value: number, otherEdge: number): number | null {
  const snapped = snapGPTImage2Edge(value)
  const bounds = gptImage2DimensionBounds(otherEdge)
  if (!snapped || !bounds) return null
  return Math.min(bounds.max, Math.max(bounds.min, snapped))
}

function invalidSize(
  error: GPTImage2SizeError,
  width?: number,
  height?: number,
  pixels?: number
): GPTImage2SizeValidation {
  return { valid: false, auto: false, width, height, pixels, experimental: false, error }
}

function roundUpTo16(value: number): number {
  return Math.ceil(value / 16) * 16
}

function roundDownTo16(value: number): number {
  return Math.floor(value / 16) * 16
}
