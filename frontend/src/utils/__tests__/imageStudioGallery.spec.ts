import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  createImageStudioArchiveItem,
  createImageStudioThumbnail,
  estimateImageStudioArchiveStorageBytes,
  isImageStudioArchiveLimitError,
  sanitizeImageStudioSource,
  type ImageStudioGalleryItem
} from '../imageStudioGallery'

const webpThumbnail = 'data:image/webp;base64,UklGRgAAAABXRUJQ'

function galleryItem(overrides: Partial<ImageStudioGalleryItem> = {}): ImageStudioGalleryItem {
  return {
    id: 'image-1',
    userId: 42,
    createdAt: 100,
    prompt: 'A city at dawn',
    revisedPrompt: 'A quiet city at dawn',
    apiKeyId: 7,
    provider: 'openai',
    model: 'gpt-image-2',
    size: '1536x1024',
    quality: 'high',
    background: 'opaque',
    outputFormat: 'png',
    count: 2,
    resultIndex: 0,
    sourceImageCount: 1,
    isEdit: true,
    imageSrc: 'data:image/png;base64,aGVsbG8=',
    ...overrides
  }
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('sanitizeImageStudioSource', () => {
  it('accepts http(s) and supported raster data URLs', () => {
    expect(sanitizeImageStudioSource('https://images.example.test/result.png')).toBe('https://images.example.test/result.png')
    expect(sanitizeImageStudioSource('data:image/png;base64,aGVsbG8=')).toBe('data:image/png;base64,aGVsbG8=')
    expect(sanitizeImageStudioSource('data:image/webp;base64,YQ==')).toBe('data:image/webp;base64,YQ==')
  })

  it('rejects executable and unsupported image sources', () => {
    expect(sanitizeImageStudioSource('javascript:alert(1)')).toBe('')
    expect(sanitizeImageStudioSource('data:image/svg+xml;base64,PHN2Zz4=')).toBe('')
    expect(sanitizeImageStudioSource('data:text/html;base64,PGgxPkJvb208L2gxPg==')).toBe('')
    expect(sanitizeImageStudioSource('https://user:secret@example.test/image.png')).toBe('')
  })
})

describe('Image Studio archives', () => {
  it('constructs a versioned whitelist record without retaining originals or request fields', () => {
    const source = {
      ...galleryItem(),
      source_images: [{ data: 'large-secret' }],
      unknownBlob: 'do-not-persist'
    } as ImageStudioGalleryItem
    const archive = createImageStudioArchiveItem(source, webpThumbnail, 200)

    expect(archive).toEqual({
      id: 'image-1',
      userId: 42,
      createdAt: 100,
      prompt: 'A city at dawn',
      revisedPrompt: 'A quiet city at dawn',
      apiKeyId: 7,
      provider: 'openai',
      model: 'gpt-image-2',
      size: '1536x1024',
      quality: 'high',
      background: 'opaque',
      outputFormat: 'png',
      count: 2,
      resultIndex: 0,
      sourceImageCount: 1,
      isEdit: true,
      recordVersion: 4,
      archivedAt: 200,
      thumbnailSrc: webpThumbnail
    })
    expect(archive).not.toHaveProperty('imageSrc')
    expect(archive).not.toHaveProperty('source_images')
    expect(archive).not.toHaveProperty('unknownBlob')
  })

  it('creates a metadata-only archive when no thumbnail is available', () => {
    expect(createImageStudioArchiveItem(galleryItem(), '', 200)).toMatchObject({
      recordVersion: 4,
      archivedAt: 200,
      thumbnailUnavailable: true
    })
  })

  it('rejects a thumbnail whose decoded payload exceeds 256 KiB', () => {
    const oversizedPayload = 'A'.repeat(Math.ceil((256 * 1024 + 1) / 3) * 4)
    let error: unknown
    try {
      createImageStudioArchiveItem(galleryItem(), `data:image/webp;base64,${oversizedPayload}`, 200)
    } catch (caught) {
      error = caught
    }
    expect(isImageStudioArchiveLimitError(error)).toBe(true)
  })

  it('recognizes archive limit errors across realms', () => {
    expect(isImageStudioArchiveLimitError({ code: 'IMAGE_STUDIO_ARCHIVE_LIMIT' })).toBe(true)
    expect(isImageStudioArchiveLimitError({ name: 'ImageStudioArchiveLimitError' })).toBe(true)
    expect(isImageStudioArchiveLimitError({ name: 'QuotaExceededError' })).toBe(true)
    expect(isImageStudioArchiveLimitError(new Error('quota'))).toBe(false)
  })

  it('rejects thumbnail data without a matching raster signature', () => {
    expect(() => createImageStudioArchiveItem(galleryItem(), 'data:image/webp;base64,dGlueQ==', 200))
      .toThrow('Invalid Image Studio thumbnail')
  })

  it('estimates the complete canonical record, including metadata and encoded thumbnail', () => {
    const withThumbnail = createImageStudioArchiveItem(galleryItem(), webpThumbnail, 200)
    const metadataOnly = createImageStudioArchiveItem(galleryItem(), '', 200)
    const longerPrompt = createImageStudioArchiveItem(galleryItem({ prompt: 'A'.repeat(2048) }), '', 200)

    expect(estimateImageStudioArchiveStorageBytes(withThumbnail)).toBeGreaterThan(estimateImageStudioArchiveStorageBytes(metadataOnly))
    expect(estimateImageStudioArchiveStorageBytes(longerPrompt)).toBeGreaterThan(estimateImageStudioArchiveStorageBytes(metadataOnly))
  })
})

describe('createImageStudioThumbnail', () => {
  it('scales the longest edge to 360 and prefers a bounded WebP payload', async () => {
    const close = vi.fn()
    vi.stubGlobal('createImageBitmap', vi.fn().mockResolvedValue({ width: 720, height: 360, close }))
    const drawImage = vi.fn()
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({
      clearRect: vi.fn(),
      drawImage
    } as unknown as CanvasRenderingContext2D)
    vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation((callback, mimeType) => {
      callback(new Blob([new Uint8Array([82, 73, 70, 70, 0, 0, 0, 0, 87, 69, 66, 80])], { type: mimeType || 'image/png' }))
    })

    const thumbnail = await createImageStudioThumbnail('data:image/png;base64,YQ==')

    expect(thumbnail).toBe(webpThumbnail)
    expect(drawImage).toHaveBeenCalledWith(expect.anything(), 0, 0, 360, 180)
    expect(close).toHaveBeenCalledTimes(1)
  })

  it('rejects unsafe input before attempting to decode it', async () => {
    await expect(createImageStudioThumbnail('data:image/svg+xml;base64,PHN2Zz4=')).rejects.toThrow('Unsafe image source')
  })
})
