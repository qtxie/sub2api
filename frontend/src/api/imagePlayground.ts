import { apiClient } from './client'

const imageGenerationTimeoutMs = 5 * 60 * 1000

export type ImageOutputFormat = 'png' | 'jpeg' | 'webp'
export type ImageQuality = 'low' | 'medium' | 'high'
export type ImageBackground = 'transparent' | 'opaque'

export interface ImagePlaygroundGenerationRequest {
  api_key_id: number
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
  revised_prompt?: string
}

export interface ImagePlaygroundGenerationResponse {
  created?: number
  data: ImagePlaygroundImage[]
}

export async function generateImage(
  payload: ImagePlaygroundGenerationRequest,
  signal?: AbortSignal
): Promise<ImagePlaygroundGenerationResponse> {
  const { data } = await apiClient.post<ImagePlaygroundGenerationResponse>(
    '/image-playground/generations',
    payload,
    { signal, timeout: imageGenerationTimeoutMs }
  )
  return data
}

export default { generateImage }
