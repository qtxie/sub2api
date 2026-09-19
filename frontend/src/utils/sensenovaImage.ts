/**
 * SenseNova (商汤日日新) U1.5 image size rules.
 *
 * Mirrors `gptImage2.ts` so Image Studio can validate custom dimensions per
 * provider. SenseNova accepts `auto` or `WIDTHxHEIGHT` where both edges are
 * multiples of 32 within 512–4096 and the aspect ratio stays within 3:1.
 * See https://platform.sensenova.cn/docs (SenseNova U1.5 · 同步图片生成).
 */

export const SENSENOVA_MIN_EDGE = 512
export const SENSENOVA_MAX_EDGE = 4_096
export const SENSENOVA_SIZE_STEP = 32

export const SENSENOVA_SIZE_PRESETS = [
  '1024x1024',
  '2048x2048',
  '2720x1536',
  '1536x2720',
  '1664x2496',
  '2496x1664',
  '4096x4096'
] as const

export type SensenovaSizeError = 'format' | 'multiple' | 'edge' | 'ratio'

export interface SensenovaSizeValidation {
  valid: boolean
  auto: boolean
  // 与 GPTImage2SizeValidation 对齐，供模板共用；SenseNova 无实验性尺寸档。
  experimental: boolean
  width?: number
  height?: number
  error?: SensenovaSizeError
}

export interface SensenovaDimensionBounds {
  min: number
  max: number
}

export function validateSensenovaSize(value: string): SensenovaSizeValidation {
  const normalized = value.trim().toLowerCase()
  if (normalized === 'auto') return { valid: true, auto: true, experimental: false }

  const match = /^(\d+)x(\d+)$/.exec(normalized)
  if (!match) return invalidSize('format')

  const width = Number(match[1])
  const height = Number(match[2])
  if (!Number.isSafeInteger(width) || !Number.isSafeInteger(height) || width <= 0 || height <= 0) {
    return invalidSize('format')
  }
  if (width % SENSENOVA_SIZE_STEP !== 0 || height % SENSENOVA_SIZE_STEP !== 0) {
    return invalidSize('multiple', width, height)
  }
  if (width < SENSENOVA_MIN_EDGE || height < SENSENOVA_MIN_EDGE ||
      width > SENSENOVA_MAX_EDGE || height > SENSENOVA_MAX_EDGE) {
    return invalidSize('edge', width, height)
  }

  const shortEdge = Math.min(width, height)
  const longEdge = Math.max(width, height)
  if (longEdge > shortEdge * 3) return invalidSize('ratio', width, height)

  return { valid: true, auto: false, width, height, experimental: false }
}

export function snapSensenovaEdge(value: number): number | null {
  if (!Number.isFinite(value) || value <= 0) return null
  const snapped = Math.round(value / SENSENOVA_SIZE_STEP) * SENSENOVA_SIZE_STEP
  return Math.min(SENSENOVA_MAX_EDGE, Math.max(SENSENOVA_SIZE_STEP, snapped))
}

export function sensenovaDimensionBounds(otherEdge: number): SensenovaDimensionBounds | null {
  const normalizedOther = snapSensenovaEdge(otherEdge)
  if (!normalizedOther) return null

  const min = roundUpToStep(Math.max(SENSENOVA_MIN_EDGE, normalizedOther / 3))
  const max = roundDownToStep(Math.min(SENSENOVA_MAX_EDGE, normalizedOther * 3))
  return min <= max ? { min, max } : null
}

export function normalizeSensenovaDimension(value: number, otherEdge: number): number | null {
  const snapped = snapSensenovaEdge(value)
  const bounds = sensenovaDimensionBounds(otherEdge)
  if (!snapped || !bounds) return null
  return Math.min(bounds.max, Math.max(bounds.min, snapped))
}

function invalidSize(error: SensenovaSizeError, width?: number, height?: number): SensenovaSizeValidation {
  return { valid: false, auto: false, width, height, experimental: false, error }
}

function roundUpToStep(value: number): number {
  return Math.ceil(value / SENSENOVA_SIZE_STEP) * SENSENOVA_SIZE_STEP
}

function roundDownToStep(value: number): number {
  return Math.floor(value / SENSENOVA_SIZE_STEP) * SENSENOVA_SIZE_STEP
}
