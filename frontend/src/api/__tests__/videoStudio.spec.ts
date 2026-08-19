import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))

vi.mock('../client', () => ({ apiClient: { get, post } }))

import {
  generateVideo,
  getVideoContent,
  getVideoStatus,
  getVideoStudioCapabilities,
  getVideoStudioPricing
} from '../videoStudio'

describe('Video Studio API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('normalizes the server-advertised fixed model capabilities', async () => {
    post.mockResolvedValue({ data: {
      provider: 'grok',
      model: 'grok-imagine-video-1.5',
      label: 'Grok Imagine Video 1.5',
      min_duration: 1,
      max_duration: 15,
      default_duration: 8,
      aspect_ratios: ['1:1', '16:9', '9:16', '4:3', '3:4', '3:2', '2:3'],
      resolutions: ['480p', '720p', '1080p'],
      max_input_images: 1,
      input_image_mime_types: ['image/png', 'image/jpeg', 'image/webp']
    } })

    const result = await getVideoStudioCapabilities(7)

    expect(post).toHaveBeenCalledWith('/video-studio/capabilities', { api_key_id: 7 }, { signal: undefined })
    expect(result.model).toBe('grok-imagine-video-1.5')
    expect(result.default_duration).toBe(8)
    expect(result.aspect_ratios).toHaveLength(7)
    expect(result.resolutions).toEqual(['480p', '720p', '1080p'])
    expect(result.max_input_images).toBe(1)
    expect(result.input_image_mime_types).toEqual(['image/png', 'image/jpeg', 'image/webp'])
  })

  it('posts only api_key_id and duration for pricing', async () => {
    post.mockResolvedValue({ data: {
      currency: 'USD', pricing_kind: 'fixed', duration: 8,
      prices: [{ resolution: '720p', unit_price: 0.14, total_price: 1.12 }]
    } })

    await expect(getVideoStudioPricing(7, 8)).resolves.toMatchObject({
      duration: 8,
      prices: [{ resolution: '720p', unit_price: 0.14, total_price: 1.12 }]
    })
    expect(post).toHaveBeenCalledWith('/video-studio/pricing', { api_key_id: 7, duration: 8 }, { signal: undefined })
  })

  it('requests an image-input quote only when a starting frame is selected', async () => {
    post.mockResolvedValue({ data: {
      currency: 'USD', pricing_kind: 'fixed', duration: 8,
      prices: [{ resolution: '720p', unit_price: 0.14125, total_price: 1.13 }]
    } })

    await getVideoStudioPricing(7, 8, true)

    expect(post).toHaveBeenCalledWith(
      '/video-studio/pricing',
      { api_key_id: 7, duration: 8, has_source_image: true },
      { signal: undefined }
    )
  })

  it('sends the strict generation payload without a model or unsupported fields', async () => {
    post.mockResolvedValue({ data: { request_id: 'video-task-1' } })
    const payload = {
      api_key_id: 7,
      prompt: 'A quiet city at dawn',
      duration: 8,
      aspect_ratio: '16:9' as const,
      resolution: '480p' as const
    }

    await expect(generateVideo(payload)).resolves.toEqual({ request_id: 'video-task-1', status: 'pending' })
    expect(post).toHaveBeenCalledWith(
      '/video-studio/generations',
      payload,
      { signal: undefined, timeout: 600000 }
    )
    expect(post.mock.calls[0][1]).not.toHaveProperty('model')
    expect(post.mock.calls[0][1]).not.toHaveProperty('quality')
    expect(post.mock.calls[0][1]).not.toHaveProperty('n')
  })

  it('sends one source image through the panel contract without provider-only modes', async () => {
    post.mockResolvedValue({ data: { request_id: 'video-task-image' } })
    const payload = {
      api_key_id: 7,
      prompt: 'The clouds drift slowly',
      duration: 6,
      aspect_ratio: '16:9' as const,
      resolution: '1080p' as const,
      source_image: { mime_type: 'image/png' as const, data: 'iVBORw0KGgo=' }
    }

    await generateVideo(payload)

    expect(post).toHaveBeenCalledWith(
      '/video-studio/generations',
      payload,
      { signal: undefined, timeout: 600000 }
    )
    expect(post.mock.calls[0][1]).not.toHaveProperty('image')
    expect(post.mock.calls[0][1]).not.toHaveProperty('reference_images')
  })

  it('encodes task IDs, clamps progress, and ignores relayed content URLs', async () => {
    get.mockResolvedValue({ data: {
      request_id: 'video/task 1', status: 'done', model: 'grok-imagine-video-1.5',
      progress: 120,
      video: { content_url: '/api/video-studio/videos/task/content', url: 'https://signed.example/video.mp4', duration: 8 }
    } })

    await expect(getVideoStatus(7, 'video/task 1')).resolves.toEqual({
      request_id: 'video/task 1', status: 'done', model: 'grok-imagine-video-1.5', progress: 100, video: { duration: 8 }
    })
    expect(get).toHaveBeenCalledWith('/video-studio/videos/video%2Ftask%201', {
      params: { api_key_id: 7 }, signal: undefined
    })
  })

  it('loads authenticated MP4 content as a Blob', async () => {
    const source = new Blob(['video'], { type: 'video/mp4' })
    get.mockResolvedValue({ data: source, headers: { 'content-type': 'video/mp4' } })

    await expect(getVideoContent(7, 'task-1')).resolves.toBe(source)
    expect(get).toHaveBeenCalledWith('/video-studio/videos/task-1/content', expect.objectContaining({
      params: { api_key_id: 7 }, responseType: 'blob'
    }))
  })
})
