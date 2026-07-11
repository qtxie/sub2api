import { apiClient } from './client'

const imageGenerationTimeoutMs = 5 * 60 * 1000

export type ImageOutputFormat = 'png' | 'jpeg' | 'webp'
export type ImageQuality = 'low' | 'medium' | 'high'
export type ImageBackground = 'auto' | 'opaque'

export interface ImagePlaygroundGenerationRequest {
  api_key_id: number
  model?: string
  prompt: string
  size?: string
  quality?: ImageQuality
  background?: ImageBackground
  output_format?: ImageOutputFormat
  n: number
}

export interface ImagePlaygroundImage {
  b64_json?: string
  url?: string
  mime_type?: string
  revised_prompt?: string
}

export interface ImagePlaygroundGenerationResponse {
  created?: number
  data: ImagePlaygroundImage[]
}

interface ImageStreamPayload {
  type?: string
  created?: number
  created_at?: number
  b64_json?: string
  url?: string
  revised_prompt?: string
  data?: unknown
  response?: unknown
  error?: unknown
  message?: string
  code?: number
}

export async function generateImage(
  payload: ImagePlaygroundGenerationRequest,
  signal?: AbortSignal
): Promise<ImagePlaygroundGenerationResponse> {
  const { data } = await apiClient.post<ImagePlaygroundGenerationResponse | string>(
    '/image-playground/generations',
    payload,
    { signal, timeout: imageGenerationTimeoutMs }
  )
  return parseImagePlaygroundResponse(data)
}

export function parseImagePlaygroundResponse(
  response: ImagePlaygroundGenerationResponse | string
): ImagePlaygroundGenerationResponse {
  if (typeof response !== 'string') return normalizeJSONResponse(response)

  const text = response.trim()
  if (!text) throw new Error('Image gateway returned an empty response')
  if (text.startsWith('{')) {
    return normalizeJSONResponse(JSON.parse(text))
  }

  let created: number | undefined
  const images: ImagePlaygroundImage[] = []
  for (const block of text.split(/\r?\n\r?\n/)) {
    const lines = block.split(/\r?\n/)
    const eventName = lines
      .find((line) => line.startsWith('event:'))
      ?.slice(6).trim()
    const dataLines = lines
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).replace(/^ /, ''))
    if (dataLines.length === 0) continue

    const eventData = dataLines.join('\n').trim()
    if (!eventData || eventData === '[DONE]') continue
    const payload = JSON.parse(eventData) as ImageStreamPayload
    const eventError = imageStreamError(payload) ||
      (eventName === 'error' ? stringValue(payload.message) || 'Image generation failed' : '')
    if (eventError) throw new Error(eventError)

    const eventCreated = numericTimestamp(payload.created_at ?? payload.created)
    if (eventCreated !== undefined) created = eventCreated

    const partialEvent = payload.type?.includes('partial_image') || eventName?.includes('partial_image')
    if (payload.type === 'image_generation.completed' || eventName === 'image_generation.completed' || (!partialEvent && hasImage(payload))) {
      const image = normalizeImage(payload)
      if (image) images.push(image)
      continue
    }
    if (payload.type === 'response.completed') {
      const completed = normalizeResponsesCompletion(payload.response)
      if (completed.created !== undefined) created = completed.created
      images.push(...completed.data)
      continue
    }
    if (Array.isArray(payload.data)) {
      images.push(...normalizeImageArray(payload.data))
    }
  }

  if (images.length === 0) throw new Error('Image gateway stream completed without an image')
  return { created, data: images }
}

function normalizeJSONResponse(value: unknown): ImagePlaygroundGenerationResponse {
  if (!value || typeof value !== 'object') throw new Error('Image gateway returned an invalid response')
  const payload = value as ImageStreamPayload
  const payloadError = imageStreamError(payload)
  if (payloadError) throw new Error(payloadError)

  if (payload.code === 0 && payload.data && !Array.isArray(payload.data)) {
    return normalizeJSONResponse(payload.data)
  }
  if (Array.isArray(payload.data)) {
    const data = normalizeImageArray(payload.data)
    if (data.length === 0) throw new Error('Image gateway returned no usable images')
    return { created: numericTimestamp(payload.created ?? payload.created_at), data }
  }
  if (payload.type === 'response.completed') return normalizeResponsesCompletion(payload.response)
  throw new Error('Image gateway returned an invalid response')
}

function normalizeResponsesCompletion(value: unknown): ImagePlaygroundGenerationResponse {
  if (!value || typeof value !== 'object') throw new Error('Image gateway returned an invalid completion event')
  const response = value as Record<string, unknown>
  const data = normalizeImageArray(Array.isArray(response.output) ? response.output : [])
  if (data.length === 0) throw new Error('Image gateway stream completed without an image')
  return { created: numericTimestamp(response.created_at ?? response.created), data }
}

function normalizeImageArray(values: unknown[]): ImagePlaygroundImage[] {
  return values.map(normalizeImage).filter((image): image is ImagePlaygroundImage => image !== null)
}

function normalizeImage(value: unknown): ImagePlaygroundImage | null {
  if (!value || typeof value !== 'object') return null
  const image = value as Record<string, unknown>
  const b64 = stringValue(image.b64_json) || stringValue(image.result)
  const url = stringValue(image.url)
  const revisedPrompt = stringValue(image.revised_prompt)
  const mimeType = stringValue(image.mime_type)
  if (!b64 && !url) return null
  return {
    ...(b64 ? { b64_json: b64 } : {}),
    ...(url ? { url } : {}),
    ...(mimeType ? { mime_type: mimeType } : {}),
    ...(revisedPrompt ? { revised_prompt: revisedPrompt } : {}),
  }
}

function hasImage(value: ImageStreamPayload): boolean {
  return Boolean(stringValue(value.b64_json) || stringValue(value.url))
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

export default { generateImage }
