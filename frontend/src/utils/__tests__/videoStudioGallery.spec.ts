import { describe, expect, it } from 'vitest'
import {
  expireStaleVideoStudioTasks,
  normalizeVideoStudioGalleryItem,
  VIDEO_STUDIO_TASK_BINDING_MS,
  videoStudioTaskID
} from '../videoStudioGallery'

function task(overrides: Record<string, unknown> = {}) {
  return {
    id: 'ignored', requestId: 'request-1', userId: 42, apiKeyId: 7,
    createdAt: 100, updatedAt: 200, prompt: 'A city at dawn', model: 'wrong-model',
    duration: 8, aspectRatio: '16:9', resolution: '720p', status: 'pending',
    trackingPaused: false, signedUrl: 'https://signed.example/video.mp4', ...overrides
  }
}

describe('Video Studio gallery normalization', () => {
  it('derives a user-scoped ID and never retains signed URL fields', () => {
    const result = normalizeVideoStudioGalleryItem(task())

    expect(result).toMatchObject({
      id: '42:request-1', requestId: 'request-1', model: 'grok-imagine-video-1.5'
    })
    expect(result).not.toHaveProperty('signedUrl')
    expect(videoStudioTaskID(42, ' request-1 ')).toBe('42:request-1')
  })

  it('retains only MP4 blobs and rejects invalid provider settings', () => {
    const mp4 = new Blob(['video'], { type: 'video/mp4' })
    expect(normalizeVideoStudioGalleryItem(task({ status: 'done', videoBlob: mp4 }))?.videoBlob).toBe(mp4)
    expect(normalizeVideoStudioGalleryItem(task({ videoBlob: new Blob(['x'], { type: 'text/html' }) }))).not.toHaveProperty('videoBlob')
    expect(normalizeVideoStudioGalleryItem(task({ resolution: '4k' }))).toBeNull()
    expect(normalizeVideoStudioGalleryItem(task({ duration: 16 }))).toBeNull()
  })

  it('allows an image-only task without retaining the source image data', () => {
    const result = normalizeVideoStudioGalleryItem(task({
      prompt: '',
      hasSourceImage: true,
      sourceImage: { mime_type: 'image/png', data: 'raw-base64' }
    }))

    expect(result).toMatchObject({ prompt: '', hasSourceImage: true })
    expect(result).not.toHaveProperty('sourceImage')
    expect(normalizeVideoStudioGalleryItem(task({ prompt: '' }))).toBeNull()
  })

  it('clamps progress and expires pending tasks past the 24-hour binding', () => {
    expect(normalizeVideoStudioGalleryItem(task({ progress: -5 }))?.progress).toBe(0)
    expect(normalizeVideoStudioGalleryItem(task({ progress: 130 }))?.progress).toBe(100)

    const createdAt = 10_000
    const item = normalizeVideoStudioGalleryItem(task({ createdAt, updatedAt: createdAt, progress: 42 }))!
    const restored = expireStaleVideoStudioTasks([item], createdAt + VIDEO_STUDIO_TASK_BINDING_MS)
    expect(restored.expired).toHaveLength(1)
    expect(restored.items[0]).toMatchObject({ status: 'expired', trackingPaused: true })
    expect(restored.items[0]).not.toHaveProperty('progress')
  })
})
