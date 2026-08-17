import { describe, expect, it } from 'vitest'
import { parseImageStudioResponse } from '../imageStudio'

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
