import { beforeEach, describe, expect, it, vi } from 'vitest'

const post = vi.hoisted(() => vi.fn())

vi.mock('../client', () => ({
  apiClient: { post }
}))

import { generateImage, getImagePricing, parseImagePlaygroundResponse } from '../imagePlayground'

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

  it('loads effective pricing for an image-enabled API key', async () => {
    const signal = new AbortController().signal
    post.mockResolvedValue({
      data: {
        currency: 'USD',
        pricing_kind: 'fixed',
        prices: [{ size: '1024x1024', billing_tier: '1K', pricing_kind: 'fixed', unit_price: 0.1 }]
      }
    })

    const result = await getImagePricing({ api_key_id: 7 }, signal)

    expect(post).toHaveBeenCalledWith('/image-playground/pricing', { api_key_id: 7 }, { signal })
    expect(result.prices[0].unit_price).toBe(0.1)
  })
})

describe('parseImagePlaygroundResponse', () => {
  it('keeps ordinary JSON responses compatible', () => {
    expect(parseImagePlaygroundResponse({
      created: 1710000000,
      data: [{ b64_json: 'final', revised_prompt: 'a revised prompt' }],
    })).toEqual({
      created: 1710000000,
      data: [{ b64_json: 'final', revised_prompt: 'a revised prompt' }],
    })
  })

  it('collects completed images and ignores partial images', () => {
    const response = parseImagePlaygroundResponse(
      'event: image_generation.partial_image\n' +
      'data: {"type":"image_generation.partial_image","b64_json":"partial"}\n\n' +
      ':\n\n' +
      'event: image_generation.completed\n' +
      'data: {"type":"image_generation.completed","created_at":1710000001,"b64_json":"final-a","revised_prompt":"first"}\n\n' +
      'event: image_generation.completed\n' +
      'data: {"type":"image_generation.completed","created_at":1710000001,"url":"https://example.com/final-b.png"}\n\n' +
      'data: [DONE]\n\n'
    )

    expect(response).toEqual({
      created: 1710000001,
      data: [
        { b64_json: 'final-a', revised_prompt: 'first' },
        { url: 'https://example.com/final-b.png' },
      ],
    })
  })

  it('parses multiline completion events', () => {
    const response = parseImagePlaygroundResponse(
      'event: image_generation.completed\n' +
      'data: {"type":"image_generation.completed",\n' +
      'data: "created_at":1710000002,"b64_json":"multiline"}\n\n'
    )

    expect(response).toEqual({ created: 1710000002, data: [{ b64_json: 'multiline' }] })
  })

  it('normalizes Responses API completion events', () => {
    const response = parseImagePlaygroundResponse(
      'data: {"type":"response.completed","response":{"created_at":1710000003,"output":[{"type":"image_generation_call","result":"final"}]}}\n\n'
    )

    expect(response).toEqual({ created: 1710000003, data: [{ b64_json: 'final' }] })
  })

  it('surfaces errors delivered after a stream has started', () => {
    expect(() => parseImagePlaygroundResponse(
      'event: error\n' +
      'data: {"type":"error","error":{"message":"upstream timed out"}}\n\n'
    )).toThrow('upstream timed out')
  })

  it('rejects streams that end without a completed image', () => {
    expect(() => parseImagePlaygroundResponse(
      'data: {"type":"image_generation.partial_image","b64_json":"partial"}\n\n' +
      'data: [DONE]\n\n'
    )).toThrow('completed without an image')
  })
})
