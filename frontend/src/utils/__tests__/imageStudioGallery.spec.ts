import { describe, expect, it } from 'vitest'
import { sanitizeImageStudioSource } from '../imageStudioGallery'

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
