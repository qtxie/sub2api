import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  savedMessageFromChatStreamError,
  streamConversationMessage,
} from '@/api/chat'

describe('chat stream parsing', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('streams backend conversation deltas and resolves the saved message', async () => {
    const encoder = new TextEncoder()
    const savedMessage = {
      id: 9,
      conversation_id: 7,
      user_id: 3,
      role: 'assistant',
      content: 'hello world',
      status: 'complete',
      error_message: '',
      metadata: {},
      created_at: 1,
      updated_at: 1,
    }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(new ReadableStream({
      start(controller) {
        controller.enqueue(encoder.encode('event: delta\ndata: {"type":"delta","content":"hello "}\n\n'))
        controller.enqueue(encoder.encode(`event: done\ndata: ${JSON.stringify({ type: 'done', message: savedMessage })}\n\n`))
        controller.close()
      },
    }), { status: 200, headers: { 'Content-Type': 'text/event-stream' } })))

    const deltas: string[] = []
    const result = await streamConversationMessage({
      conversationId: 7,
      onDelta: (delta) => deltas.push(delta),
    })

    expect(deltas).toEqual(['hello '])
    expect(result).toEqual(savedMessage)
  })

  it('sends transient attachments in the backend stream request body', async () => {
    const encoder = new TextEncoder()
    const savedMessage = {
      id: 9,
      conversation_id: 7,
      user_id: 3,
      role: 'assistant',
      content: 'described',
      status: 'complete',
      error_message: '',
      metadata: {},
      created_at: 1,
      updated_at: 1,
    }
    const fetchMock = vi.fn().mockResolvedValue(new Response(new ReadableStream({
      start(controller) {
        controller.enqueue(encoder.encode(`event: done\ndata: ${JSON.stringify({ type: 'done', message: savedMessage })}\n\n`))
        controller.close()
      },
    }), { status: 200, headers: { 'Content-Type': 'text/event-stream' } }))
    vi.stubGlobal('fetch', fetchMock)

    await streamConversationMessage({
      conversationId: 7,
      attachments: [{
        type: 'image',
        name: 'a.png',
        mime_type: 'image/png',
        size: 3,
        data_url: 'data:image/png;base64,QUJD',
      }],
    })

    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect(JSON.parse(init.body as string)).toEqual({
      attachments: [{
        type: 'image',
        name: 'a.png',
        mime_type: 'image/png',
        size: 3,
        data_url: 'data:image/png;base64,QUJD',
      }],
    })
  })

  it('exposes backend-saved error messages from stream errors', async () => {
    const encoder = new TextEncoder()
    const savedMessage = {
      id: 10,
      conversation_id: 7,
      user_id: 3,
      role: 'assistant',
      content: 'partial',
      status: 'error',
      error_message: 'upstream failed',
      metadata: {},
      created_at: 1,
      updated_at: 1,
    }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(new ReadableStream({
      start(controller) {
        controller.enqueue(encoder.encode('event: delta\ndata: {"type":"delta","content":"partial"}\n\n'))
        controller.enqueue(encoder.encode(`event: error\ndata: ${JSON.stringify({ type: 'error', error: 'upstream failed', saved_message: savedMessage })}\n\n`))
        controller.close()
      },
    }), { status: 200, headers: { 'Content-Type': 'text/event-stream' } })))

    let thrown: unknown
    await streamConversationMessage({ conversationId: 7 }).catch((err) => {
      thrown = err
    })

    expect(thrown).toBeInstanceOf(Error)
    expect(savedMessageFromChatStreamError(thrown)).toEqual(savedMessage)
  })
})
