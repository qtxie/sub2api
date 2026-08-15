import { apiClient } from './client'
import { sanitizeImageStudioSource } from '@/utils/imageStudioGallery'

const imageGenerationTimeoutMs = 10 * 60 * 1000

export type ImageOutputFormat = 'png' | 'jpeg' | 'webp'
export type ImageQuality = 'auto' | 'low' | 'medium' | 'high'
export type ImageBackground = 'auto' | 'opaque'

export interface ImageStudioGenerationRequest {
  api_key_id: number
  prompt: string
  size: string
  quality: ImageQuality
  background: ImageBackground
  output_format: ImageOutputFormat
  n: number
}

export interface ImageStudioImage {
  b64_json?: string
  url?: string
  mime_type?: string
  revised_prompt?: string
}

export interface ImageStudioGenerationResponse {
  created?: number
  data: ImageStudioImage[]
}

export type ImagePricingKind = 'fixed' | 'usage_based'

export interface ImageStudioResolutionPrice {
  size: string
  billing_tier: string
  pricing_kind: ImagePricingKind
  unit_price: number | null
}

export interface ImageStudioPricingResponse {
  currency: string
  pricing_kind: ImagePricingKind
  prices: ImageStudioResolutionPrice[]
}

export async function getImageStudioPricing(apiKeyId: number, signal?: AbortSignal): Promise<ImageStudioPricingResponse> {
  const { data } = await apiClient.post<ImageStudioPricingResponse>(
    '/image-studio/pricing',
    { api_key_id: apiKeyId },
    { signal }
  )
  return data
}

export async function generateImage(
  payload: ImageStudioGenerationRequest,
  signal?: AbortSignal
): Promise<ImageStudioGenerationResponse> {
  const { data } = await apiClient.post<ImageStudioGenerationResponse | string>(
    '/image-studio/generations',
    payload,
    { signal, timeout: imageGenerationTimeoutMs }
  )
  return parseImageStudioResponse(data)
}

interface ImageStreamPayload {
  type?: string
  created?: number
  created_at?: number
  b64_json?: string
  result?: string
  url?: string
  output_format?: string
  mime_type?: string
  revised_prompt?: string
  data?: unknown
  response?: unknown
  error?: unknown
  message?: string
  code?: number
}

export function parseImageStudioResponse(response: ImageStudioGenerationResponse | string): ImageStudioGenerationResponse {
  if (typeof response !== 'string') return normalizeJSONResponse(response)

  const body = response.trim()
  if (!body) throw new Error('Image gateway returned an empty response')
  if (body.startsWith('{')) {
    try {
      return normalizeJSONResponse(JSON.parse(body))
    } catch (error) {
      if (error instanceof Error && error.message.startsWith('Image gateway')) throw error
      throw new Error('Image gateway returned invalid JSON')
    }
  }

  let created: number | undefined
  const images: ImageStudioImage[] = []
  for (const block of body.split(/\r?\n\r?\n/)) {
    const lines = block.split(/\r?\n/)
    const eventName = lines.find((line) => line.startsWith('event:'))?.slice(6).trim()
    const dataLines = lines
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).replace(/^ /, ''))
    if (dataLines.length === 0) continue
    const eventData = dataLines.join('\n').trim()
    if (!eventData || eventData === '[DONE]') continue

    let payload: ImageStreamPayload
    try {
      payload = JSON.parse(eventData) as ImageStreamPayload
    } catch {
      throw new Error('Image gateway returned an invalid stream event')
    }
    const eventError = imageStreamError(payload) ||
      (eventName === 'error' ? stringValue(payload.message) || 'Image generation failed' : '')
    if (eventError) throw new Error(eventError)

    const eventCreated = numericTimestamp(payload.created_at ?? payload.created)
    if (eventCreated !== undefined) created = eventCreated
    const partial = payload.type?.includes('partial_image') || eventName?.includes('partial_image')
    if (payload.type === 'response.completed') {
      const completed = normalizeResponsesCompletion(payload.response)
      if (completed.created !== undefined) created = completed.created
      images.push(...completed.data)
    } else if (!partial && (payload.type === 'image_generation.completed' || eventName === 'image_generation.completed' || hasImage(payload))) {
      const image = normalizeImage(payload)
      if (image) images.push(image)
    } else if (Array.isArray(payload.data)) {
      images.push(...normalizeImageArray(payload.data))
    }
  }

  if (images.length === 0) throw new Error('Image gateway stream completed without an image')
  return { created, data: images }
}

function normalizeJSONResponse(value: unknown): ImageStudioGenerationResponse {
  if (!value || typeof value !== 'object') throw new Error('Image gateway returned an invalid response')
  const payload = value as ImageStreamPayload
  const payloadError = imageStreamError(payload)
  if (payloadError) throw new Error(payloadError)
  if (payload.code === 0 && payload.data && !Array.isArray(payload.data)) return normalizeJSONResponse(payload.data)
  if (Array.isArray(payload.data)) {
    const data = normalizeImageArray(payload.data)
    if (data.length === 0) throw new Error('Image gateway returned no usable images')
    return { created: numericTimestamp(payload.created ?? payload.created_at), data }
  }
  if (payload.type === 'response.completed') return normalizeResponsesCompletion(payload.response)
  throw new Error('Image gateway returned an invalid response')
}

function normalizeResponsesCompletion(value: unknown): ImageStudioGenerationResponse {
  if (!value || typeof value !== 'object') throw new Error('Image gateway returned an invalid completion event')
  const response = value as Record<string, unknown>
  const data = normalizeImageArray(Array.isArray(response.output) ? response.output : [])
  if (data.length === 0) throw new Error('Image gateway stream completed without an image')
  return { created: numericTimestamp(response.created_at ?? response.created), data }
}

function normalizeImageArray(values: unknown[]): ImageStudioImage[] {
  return values.map(normalizeImage).filter((image): image is ImageStudioImage => image !== null)
}

function normalizeImage(value: unknown): ImageStudioImage | null {
  if (!value || typeof value !== 'object') return null
  const image = value as Record<string, unknown>
  const b64 = safeBase64Payload(stringValue(image.b64_json) || stringValue(image.result))
  const rawURL = stringValue(image.url)
  const url = rawURL ? sanitizeImageStudioSource(rawURL) : ''
  if (!b64 && !url) return null
  const mimeType = normalizeMimeType(stringValue(image.mime_type), stringValue(image.output_format))
  const revisedPrompt = stringValue(image.revised_prompt)
  return {
    ...(b64 ? { b64_json: b64 } : {}),
    ...(url ? { url } : {}),
    ...(mimeType ? { mime_type: mimeType } : {}),
    ...(revisedPrompt ? { revised_prompt: revisedPrompt } : {})
  }
}

function normalizeMimeType(mimeType: string, outputFormat: string): string {
  const normalized = mimeType.toLowerCase() === 'image/jpg' ? 'image/jpeg' : mimeType.toLowerCase()
  if (['image/png', 'image/jpeg', 'image/webp'].includes(normalized)) return normalized
  const format = outputFormat.toLowerCase()
  if (format === 'jpg' || format === 'jpeg') return 'image/jpeg'
  if (format === 'webp') return 'image/webp'
  if (format === 'png') return 'image/png'
  return ''
}

function safeBase64Payload(value: string): string {
  if (!value || value.length % 4 === 1 || !/^[A-Za-z0-9+/]*={0,2}$/.test(value)) return ''
  return value
}

function hasImage(value: ImageStreamPayload): boolean {
  return Boolean(stringValue(value.b64_json) || stringValue(value.result) || stringValue(value.url))
}

function imageStreamError(payload: ImageStreamPayload): string {
  if (typeof payload.error === 'string') return payload.error.trim()
  if (payload.error && typeof payload.error === 'object') {
    return stringValue((payload.error as Record<string, unknown>).message)
  }
  if (payload.code !== undefined && payload.code !== 0) return stringValue(payload.message) || 'Image generation failed'
  return payload.type === 'error' ? stringValue(payload.message) || 'Image generation failed' : ''
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function numericTimestamp(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

export default { generateImage, getImageStudioPricing }
