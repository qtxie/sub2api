import { apiClient } from './client'

export const VIDEO_STUDIO_MODEL = 'grok-imagine-video-1.5'
export const VIDEO_STUDIO_DURATIONS = { min: 1, max: 15, default: 8 } as const
export const VIDEO_STUDIO_ASPECT_RATIOS = ['1:1', '16:9', '9:16', '4:3', '3:4', '3:2', '2:3'] as const
export const VIDEO_STUDIO_RESOLUTIONS = ['480p', '720p', '1080p'] as const

export type VideoStudioAspectRatio = typeof VIDEO_STUDIO_ASPECT_RATIOS[number]
export type VideoStudioResolution = typeof VIDEO_STUDIO_RESOLUTIONS[number]
export type VideoStudioStatus = 'pending' | 'done' | 'failed' | 'expired'

export interface VideoStudioCapabilities {
  model: typeof VIDEO_STUDIO_MODEL
  label: string
  min_duration: number
  max_duration: number
  default_duration: number
  aspect_ratios: VideoStudioAspectRatio[]
  resolutions: VideoStudioResolution[]
}

export interface VideoStudioResolutionPrice {
  resolution: VideoStudioResolution
  unit_price: number | null
  total_price: number | null
}

export interface VideoStudioPricingResponse {
  currency: string
  pricing_kind: 'fixed' | 'usage_based'
  duration: number
  prices: VideoStudioResolutionPrice[]
}

export interface VideoStudioGenerationRequest {
  api_key_id: number
  prompt: string
  duration: number
  aspect_ratio: VideoStudioAspectRatio
  resolution: VideoStudioResolution
}

export interface VideoStudioGenerationResponse {
  request_id: string
  status: VideoStudioStatus
}

export interface VideoStudioVideoMetadata {
  duration?: number
}

export interface VideoStudioStatusResponse {
  request_id: string
  status: VideoStudioStatus
  model?: string
  progress?: number
  video?: VideoStudioVideoMetadata
  error?: string
}

const videoContentTimeoutMs = 10 * 60 * 1000

export async function getVideoStudioCapabilities(
  apiKeyId: number,
  signal?: AbortSignal
): Promise<VideoStudioCapabilities> {
  const { data } = await apiClient.post<unknown>(
    '/video-studio/capabilities',
    { api_key_id: apiKeyId },
    { signal }
  )
  return normalizeCapabilities(data)
}

export async function getVideoStudioPricing(
  apiKeyId: number,
  duration: number,
  signal?: AbortSignal
): Promise<VideoStudioPricingResponse> {
  const { data } = await apiClient.post<unknown>(
    '/video-studio/pricing',
    { api_key_id: apiKeyId, duration },
    { signal }
  )
  return normalizePricing(data, duration)
}

export async function generateVideo(
  payload: VideoStudioGenerationRequest,
  signal?: AbortSignal
): Promise<VideoStudioGenerationResponse> {
  const { data } = await apiClient.post<unknown>('/video-studio/generations', payload, { signal })
  return normalizeGeneration(data)
}

export async function getVideoStatus(
  apiKeyId: number,
  requestId: string,
  signal?: AbortSignal
): Promise<VideoStudioStatusResponse> {
  const { data } = await apiClient.get<unknown>(
    `/video-studio/videos/${encodeURIComponent(requestId)}`,
    { params: { api_key_id: apiKeyId }, signal }
  )
  return normalizeStatus(data, requestId)
}

export async function getVideoContent(
  apiKeyId: number,
  requestId: string,
  signal?: AbortSignal
): Promise<Blob> {
  const response = await apiClient.get<Blob>(
    `/video-studio/videos/${encodeURIComponent(requestId)}/content`,
    {
      params: { api_key_id: apiKeyId },
      responseType: 'blob',
      signal,
      timeout: videoContentTimeoutMs
    }
  )
  const contentType = String(response.headers?.['content-type'] || response.data?.type || '')
    .split(';', 1)[0]
    .trim()
    .toLowerCase()
  if (!(response.data instanceof Blob) || (contentType && !['video/mp4', 'application/octet-stream'].includes(contentType))) {
    throw new Error('Video Studio returned invalid video content')
  }
  return response.data.type === 'video/mp4'
    ? response.data
    : new Blob([response.data], { type: 'video/mp4' })
}

function normalizeCapabilities(value: unknown): VideoStudioCapabilities {
  const payload = objectValue(value)
  const model = stringValue(payload?.model || payload?.default_model)
  if (model && model !== VIDEO_STUDIO_MODEL) throw new Error('Video Studio returned an unsupported model')

  const duration = objectValue(payload?.duration)
  const minDuration = boundedInteger(payload?.min_duration ?? duration?.min, 1, 15) || VIDEO_STUDIO_DURATIONS.min
  const maxDuration = boundedInteger(payload?.max_duration ?? duration?.max, minDuration, 15) || VIDEO_STUDIO_DURATIONS.max
  const defaultDuration = boundedInteger(
    payload?.default_duration ?? duration?.default,
    minDuration,
    maxDuration
  ) || VIDEO_STUDIO_DURATIONS.default
  const aspectRatios = enumArray(payload?.aspect_ratios, VIDEO_STUDIO_ASPECT_RATIOS)
  const resolutions = enumArray(payload?.resolutions, VIDEO_STUDIO_RESOLUTIONS)
  if (aspectRatios.length === 0 || resolutions.length === 0) {
    throw new Error('Video Studio returned no supported video options')
  }
  return {
    model: VIDEO_STUDIO_MODEL,
    label: stringValue(payload?.label) || 'Grok Imagine Video 1.5',
    min_duration: minDuration,
    max_duration: maxDuration,
    default_duration: defaultDuration,
    aspect_ratios: aspectRatios,
    resolutions
  }
}

function normalizePricing(value: unknown, duration: number): VideoStudioPricingResponse {
  const payload = objectValue(value)
  const normalizedDuration = boundedInteger(payload?.duration, 1, 15) || duration
  const rawPrices = Array.isArray(payload?.prices) ? payload.prices : []
  const prices = rawPrices
    .map((raw): VideoStudioResolutionPrice | null => {
      const price = objectValue(raw)
      const resolution = enumValue(price?.resolution || price?.size, VIDEO_STUDIO_RESOLUTIONS)
      if (!resolution) return null
      const explicitUnit = nullableNonNegativeNumber(
        price?.unit_price ?? price?.price_per_second ?? price?.unit_price_per_second
      )
      const explicitTotal = nullableNonNegativeNumber(price?.total_price ?? price?.estimated_total)
      const unitPrice = explicitUnit ?? (explicitTotal === null ? null : explicitTotal / normalizedDuration)
      return {
        resolution,
        unit_price: unitPrice,
        total_price: explicitTotal ?? (unitPrice === null ? null : unitPrice * normalizedDuration)
      }
    })
    .filter((price): price is VideoStudioResolutionPrice => price !== null)
  return {
    currency: stringValue(payload?.currency) || 'USD',
    pricing_kind: payload?.pricing_kind === 'usage_based' ? 'usage_based' : 'fixed',
    duration: normalizedDuration,
    prices
  }
}

function normalizeGeneration(value: unknown): VideoStudioGenerationResponse {
  const payload = objectValue(value)
  const requestId = stringValue(payload?.request_id || payload?.id)
  if (!validRequestId(requestId)) throw new Error('Video Studio returned no valid request ID')
  return { request_id: requestId, status: normalizeStatusValue(payload?.status, 'pending') }
}

function validRequestId(value: string): boolean {
  return value.length > 0 && value.length <= 192 && /^[A-Za-z0-9_-]+$/.test(value)
}

function normalizeStatus(value: unknown, fallbackRequestId: string): VideoStudioStatusResponse {
  const payload = objectValue(value)
  if (!payload) throw new Error('Video Studio returned an invalid status response')
  const requestId = stringValue(payload.request_id || payload.id) || fallbackRequestId
  const video = objectValue(payload.video)
  const error = typeof payload.error === 'string'
    ? payload.error.trim()
    : stringValue(objectValue(payload.error)?.message || payload.message)
  return {
    request_id: requestId,
    status: normalizeStatusValue(payload.status),
    ...(stringValue(payload.model) ? { model: stringValue(payload.model) } : {}),
    ...(clampedNumber(payload.progress, 0, 100) !== null ? { progress: clampedNumber(payload.progress, 0, 100)! } : {}),
    ...(video ? { video: { ...(positiveNumber(video.duration) ? { duration: positiveNumber(video.duration)! } : {}) } } : {}),
    ...(error ? { error } : {})
  }
}

function normalizeStatusValue(value: unknown, fallback?: VideoStudioStatus): VideoStudioStatus {
  const status = stringValue(value).toLowerCase()
  if (status === 'pending' || status === 'done' || status === 'failed' || status === 'expired') return status
  if (fallback) return fallback
  throw new Error('Video Studio returned an unsupported status')
}

function objectValue(value: unknown): Record<string, any> | null {
  return value && typeof value === 'object' ? value as Record<string, any> : null
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function positiveNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null
}

function nullableNonNegativeNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null
}

function clampedNumber(value: unknown, min: number, max: number): number | null {
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.min(max, Math.max(min, value))
    : null
}

function boundedInteger(value: unknown, min: number, max: number): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value >= min && value <= max ? value : null
}

function enumValue<T extends string>(value: unknown, allowed: readonly T[]): T | null {
  const normalized = stringValue(value)
  return allowed.includes(normalized as T) ? normalized as T : null
}

function enumArray<T extends string>(value: unknown, allowed: readonly T[]): T[] {
  if (!Array.isArray(value)) return []
  return [...new Set(value.map((item) => enumValue(item, allowed)).filter((item): item is T => item !== null))]
}

export default {
  getVideoStudioCapabilities,
  getVideoStudioPricing,
  generateVideo,
  getVideoStatus,
  getVideoContent
}
