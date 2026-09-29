import { beforeEach, describe, expect, it, vi } from 'vitest'
import { parseImageStudioResponse } from '../imageStudio'

const post = vi.hoisted(() => vi.fn())
vi.mock('../client', () => ({ apiClient: { post } }))

describe('parseImageStudioResponse', () => {
  it('extracts final images and ignores heartbeat and partial events', () => {
    const response = parseImageStudioResponse([
      ': image-studio connected',
      '',
      'event: image_generation.partial_image',
      'data: {"type":"image_generation.partial_image","b64_json":"cGFydGlhbA=="}',
      '',
      ': image-studio heartbeat',
      '',
      'event: image_generation.completed',
      'data: {"type":"image_generation.completed","b64_json":"ZmluYWw=","output_format":"webp"}',
      ''
    ].join('\n'))

    expect(response.data).toEqual([{ b64_json: 'ZmluYWw=', mime_type: 'image/webp' }])
  })

  it('extracts Responses completion output', () => {
    const response = parseImageStudioResponse('event: response.completed\ndata: {"type":"response.completed","response":{"created_at":7,"output":[{"type":"image_generation_call","result":"aGVsbG8=","output_format":"png"}]}}\n\n')
    expect(response).toEqual({ created: 7, data: [{ b64_json: 'aGVsbG8=', mime_type: 'image/png' }] })
  })

  it('extracts edit completion images and ignores edit partials', () => {
    const response = parseImageStudioResponse([
      'event: image_edit.partial_image',
      'data: {"type":"image_edit.partial_image","b64_json":"cGFydGlhbA=="}',
      '',
      'event: image_edit.completed',
      'data: {"type":"image_edit.completed","b64_json":"ZWRpdGVk","output_format":"png"}',
      ''
    ].join('\n'))

    expect(response.data).toEqual([{ b64_json: 'ZWRpdGVk', mime_type: 'image/png' }])
  })

  it('rejects unsafe upstream URLs and malformed stream events', () => {
    expect(() => parseImageStudioResponse({ data: [{ url: 'javascript:alert(1)' }] })).toThrow('no usable images')
    expect(() => parseImageStudioResponse('event: completed\ndata: not-json\n\n')).toThrow('invalid stream event')
  })

  it('surfaces structured stream errors', () => {
    expect(() => parseImageStudioResponse('event: error\ndata: {"type":"error","error":{"message":"blocked"}}\n\n')).toThrow('blocked')
  })

  it('normalizes Gemini Interactions image content blocks and ignores text blocks', () => {
    const response = parseImageStudioResponse({
      created_at: 17,
      steps: [{
        type: 'model_output',
        content: [
          { type: 'text', text: 'A revised lighthouse prompt' },
          { type: 'image', data: 'aGVsbG8=', mime_type: 'image/jpeg' }
        ]
      }]
    })

    expect(response).toEqual({
      created: 17,
      data: [{ b64_json: 'aGVsbG8=', mime_type: 'image/jpeg' }]
    })
  })
})

describe('getImageStudioCapabilities', () => {
  beforeEach(() => {
    post.mockReset()
  })

  // 2.5 的 quality 档位是 auto / xhigh / max；若被 legacy 白名单过滤掉，
  // 面板上就只剩「自动」一个选项。
  it('keeps GPT Image 2.5 quality tiers instead of dropping them', async () => {
    const { getImageStudioCapabilities } = await import('../imageStudio')
    post.mockResolvedValue({
      data: {
        provider: 'openai',
        default_model: 'gpt-image-2.5-flare',
        models: [
          {
            id: 'gpt-image-2',
            label: 'GPT Image 2',
            aspect_ratios: [],
            image_sizes: ['1024x1024'],
            resolutions: [],
            qualities: ['auto', 'low', 'medium', 'high'],
            backgrounds: ['auto', 'opaque', 'transparent'],
            output_formats: ['png', 'jpeg', 'webp'],
            max_images: 4,
            max_input_images: 16,
            supports_custom_size: true
          },
          {
            id: 'gpt-image-2.5-flare',
            label: 'GPT Image 2.5 Flare',
            aspect_ratios: [],
            image_sizes: ['1024x1024'],
            resolutions: [],
            qualities: ['auto', 'xhigh', 'max'],
            backgrounds: ['auto', 'opaque', 'transparent'],
            output_formats: ['png', 'jpeg', 'webp'],
            max_images: 4,
            max_input_images: 16,
            supports_custom_size: true
          }
        ]
      }
    })

    const result = await getImageStudioCapabilities(7)
    expect(result.default_model).toBe('gpt-image-2.5-flare')
    expect(result.models[0].qualities).toEqual(['auto', 'low', 'medium', 'high'])
    expect(result.models[1].qualities).toEqual(['auto', 'xhigh', 'max'])
  })
})
