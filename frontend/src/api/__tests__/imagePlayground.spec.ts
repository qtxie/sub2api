import { beforeEach, describe, expect, it, vi } from 'vitest'

const post = vi.hoisted(() => vi.fn())

vi.mock('../client', () => ({
  apiClient: { post }
}))

import { generateImage } from '../imagePlayground'

describe('image playground API', () => {
  beforeEach(() => post.mockReset())

  it('posts a generation request through the authenticated facade', async () => {
    const signal = new AbortController().signal
    post.mockResolvedValue({ data: { created: 1, data: [{ b64_json: 'abc' }] } })

    const result = await generateImage({
      api_key_id: 7,
      prompt: 'paper kite',
      size: '1024x1024',
      quality: 'medium',
      background: 'opaque',
      output_format: 'png',
      n: 1
    }, signal)

    expect(post).toHaveBeenCalledWith('/image-playground/generations', expect.objectContaining({
      api_key_id: 7,
      prompt: 'paper kite',
      n: 1
    }), { signal, timeout: 300000 })
    expect(result.data[0].b64_json).toBe('abc')
  })
})
