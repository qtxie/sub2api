import { apiClient } from './client'
import { sanitizeImageStudioSource } from '@/utils/imageStudioGallery'

const imageGenerationTimeoutMs = 10 * 60 * 1000

export type ImageOutputFormat = 'png' | 'jpeg' | 'webp'
export type ImageQuality = 'auto' | 'low' | 'medium' | 'high'
export type ImageBackground = 'auto' | 'opaque'
export type ImageStudioProvider = 'openai' | 'gemini' | 'grok'

export interface ImageStudioSourceImage {
  mime_type: 'image/png' | 'image/jpeg' | 'image/webp'
  /** Base64 payload without a data URL prefix. */
  data: string
}

export interface ImageStudioGenerationRequestBase {
  api_key_id: number
  prompt: string
  model: string
  source_images?: ImageStudioSourceImage[]
}

export interface OpenAIImageStudioGenerationRequest extends ImageStudioGenerationRequestBase {
  size: string
  quality: ImageQuality
  background: ImageBackground
  output_format: ImageOutputFormat
  n: number
}

export interface GeminiImageStudioGenerationRequest extends ImageStudioGenerationRequestBase {
  aspect_ratio: string
  image_size?: string
}

export interface GrokImageStudioGenerationRequest extends ImageStudioGenerationRequestBase {
  aspect_ratio: string
  resolution: string
  quality: Extract<ImageQuality, 'low' | 'medium'>
  n: number
}

export interface GrokImageStudioEditRequest extends ImageStudioGenerationRequestBase {
  source_images: ImageStudioSourceImage[]
  aspect_ratio?: string
}

export type ImageStudioGenerationRequest =
  | OpenAIImageStudioGenerationRequest
  | GeminiImageStudioGenerationRequest
  | GrokImageStudioGenerationRequest
  | GrokImageStudioEditRequest

export interface ImageStudioModelCapability {
  id: string
  label: string
  aspect_ratios: string[]
  image_sizes: string[]
  resolutions: string[]
  qualities: ImageQuality[]
  backgrounds: ImageBackground[]
  output_formats: ImageOutputFormat[]
  max_images: number
  max_input_images: number
  supports_custom_size: boolean
}

export interface ImageStudioCapabilitiesResponse {
  provider: ImageStudioProvider
  default_model: string
  models: ImageStudioModelCapability[]
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
  model?: string
  aspect_ratio?: string
  image_size?: string
  resolution?: string
}

export interface ImageStudioPricingResponse {
  currency: string
  pricing_kind: ImagePricingKind
  prices: ImageStudioResolutionPrice[]
  provider?: ImageStudioProvider
  model?: string
}

export async function getImageStudioCapabilities(
  apiKeyId: number,
  signal?: AbortSignal
): Promise<ImageStudioCapabilitiesResponse> {
  const { data } = await apiClient.post<unknown>(
    '/image-studio/capabilities',
    { api_key_id: apiKeyId },
    { signal }
  )
  return normalizeCapabilities(data)
}

export async function getImageStudioPricing(
  apiKeyId: number,
  model?: string,
  signal?: AbortSignal
): Promise<ImageStudioPricingResponse> {
  const { data } = await apiClient.post<ImageStudioPricingResponse>(
    '/image-studio/pricing',
    { api_key_id: apiKeyId, ...(model ? { model } : {}) },
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
  const geminiImages = normalizeGeminiResponse(value as Record<string, unknown>)
  if (geminiImages.length > 0) {
    return { created: numericTimestamp(payload.created ?? payload.created_at), data: geminiImages }
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
  const inlineData = objectValue(image.inlineData) || objectValue(image.inline_data)
  if (inlineData) {
    const b64 = safeBase64Payload(stringValue(inlineData.data))
    if (!b64) return null
    const mimeType = normalizeMimeType(
      stringValue(inlineData.mimeType) || stringValue(inlineData.mime_type),
      ''
    )
    if (!mimeType) return null
    return { b64_json: b64, ...(mimeType ? { mime_type: mimeType } : {}) }
  }
  const interactionData = image.type === 'image' ? stringValue(image.data) : ''
  const b64 = safeBase64Payload(stringValue(image.b64_json) || stringValue(image.result) || interactionData)
  const rawURL = stringValue(image.url)
  const url = rawURL ? sanitizeImageStudioSource(rawURL) : ''
  if (!b64 && !url) return null
  const mimeType = normalizeMimeType(stringValue(image.mime_type), stringValue(image.output_format))
  if (interactionData && !mimeType) return null
  const revisedPrompt = stringValue(image.revised_prompt)
  return {
    ...(b64 ? { b64_json: b64 } : {}),
    ...(url ? { url } : {}),
    ...(mimeType ? { mime_type: mimeType } : {}),
    ...(revisedPrompt ? { revised_prompt: revisedPrompt } : {})
  }
}

function normalizeGeminiResponse(payload: Record<string, unknown>): ImageStudioImage[] {
  const images: ImageStudioImage[] = []
  const candidates = Array.isArray(payload.candidates) ? payload.candidates : []
  for (const candidate of candidates) {
    const content = objectValue(objectValue(candidate)?.content)
    const parts = Array.isArray(content?.parts) ? content.parts : []
    images.push(...normalizeImageArray(parts))
  }

  const outputs = Array.isArray(payload.outputs)
    ? payload.outputs
    : Array.isArray(payload.output)
      ? payload.output
      : []
  images.push(...normalizeImageArray(outputs))

  const steps = Array.isArray(payload.steps) ? payload.steps : []
  for (const step of steps) {
    const content = objectValue(step)?.content
    if (Array.isArray(content)) images.push(...normalizeImageArray(content))
  }
  return images
}

function normalizeCapabilities(value: unknown): ImageStudioCapabilitiesResponse {
  const payload = objectValue(value)
  const provider = stringValue(payload?.provider)
  if (!isImageStudioProvider(provider)) throw new Error('Image Studio returned an unsupported provider')

  const rawModels = Array.isArray(payload?.models) ? payload.models : []
  const models = rawModels
    .map((raw): ImageStudioModelCapability | null => {
      const model = objectValue(raw)
      const id = stringValue(model?.id) || stringValue(model?.model)
      if (!id) return null
      const maxImages = positiveInteger(model?.max_images ?? model?.maxImages) || 1
      const maxInputImages = nonNegativeInteger(model?.max_input_images ?? model?.maxInputImages)
      return {
        id,
        label: stringValue(model?.label) || stringValue(model?.name) || id,
        aspect_ratios: stringArray(model?.aspect_ratios ?? model?.aspectRatios),
        image_sizes: stringArray(model?.image_sizes ?? model?.imageSizes),
        resolutions: stringArray(model?.resolutions),
        qualities: enumArray(model?.qualities, ['auto', 'low', 'medium', 'high'] as const),
        backgrounds: enumArray(model?.backgrounds, ['auto', 'opaque'] as const),
        output_formats: enumArray(model?.output_formats ?? model?.outputFormats, ['png', 'jpeg', 'webp'] as const),
        max_images: maxImages,
        max_input_images: maxInputImages,
        supports_custom_size: model?.supports_custom_size === true || model?.supportsCustomSize === true
      }
    })
    .filter((model): model is ImageStudioModelCapability => model !== null)
  if (models.length === 0) throw new Error('Image Studio returned no supported image models')

  const requestedDefault = stringValue(payload?.default_model ?? payload?.defaultModel)
  return {
    provider,
    default_model: models.some((model) => model.id === requestedDefault) ? requestedDefault : models[0].id,
    models
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

function objectValue(value: unknown): Record<string, any> | null {
  return value && typeof value === 'object' ? value as Record<string, any> : null
}

function stringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return [...new Set(value.map(stringValue).filter(Boolean))]
}

function enumArray<T extends string>(value: unknown, allowed: readonly T[]): T[] {
  const values = stringArray(value)
  return values.filter((item): item is T => allowed.includes(item as T))
}

function positiveInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : null
}

function nonNegativeInteger(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : 0
}

function isImageStudioProvider(value: string): value is ImageStudioProvider {
  return value === 'openai' || value === 'gemini' || value === 'grok'
}

export default { generateImage, getImageStudioCapabilities, getImageStudioPricing }
