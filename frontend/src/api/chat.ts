import { apiClient, buildApiUrl } from './client'
import { getLocale } from '@/i18n'
import type { PaginatedResponse } from '@/types'

export type ChatRole = 'user' | 'assistant'
export type ChatMessageStatus = 'complete' | 'error' | 'cancelled'
export type ChatReasoningEffort = '' | 'auto' | 'none' | 'minimal' | 'low' | 'medium' | 'high' | 'max' | 'ultra' | 'x-high' | 'xhigh'

export interface ChatMessage {
  id: number
  conversation_id: number
  user_id: number
  role: ChatRole
  content: string
  status: ChatMessageStatus
  error_message: string
  metadata: Record<string, unknown>
  created_at: number
  updated_at: number
}

export interface ChatConversation {
  id: number
  user_id: number
  title: string
  api_key_id: number | null
  model: string
  system_prompt: string
  reasoning_effort: ChatReasoningEffort
  message_count: number
  messages?: ChatMessage[]
  created_at: number
  updated_at: number
}

export interface CreateChatConversationRequest {
  title?: string
  api_key_id?: number | null
  model?: string
  system_prompt?: string
  reasoning_effort?: ChatReasoningEffort
}

export interface UpdateChatConversationRequest extends CreateChatConversationRequest {}

export interface CreateChatMessageRequest {
  role: ChatRole
  content: string
  status?: ChatMessageStatus
  error_message?: string
  metadata?: Record<string, unknown>
}

export interface ChatModelListResponse {
  models: string[]
}

export interface ChatExportResponse {
  version: number
  exported_at: number
  conversations: ChatConversation[]
}

export interface ChatStreamEvent {
  type: 'delta' | 'done' | 'error'
  content?: string
  message?: ChatMessage
  saved_message?: ChatMessage
  error?: string
}

export interface ChatStreamAttachment {
  type: 'image' | 'file'
  name: string
  mime_type: string
  size: number
  data_url?: string
  text?: string
}

export class ChatStreamError extends Error {
  savedMessage?: ChatMessage

  constructor(message: string, savedMessage?: ChatMessage) {
    super(message)
    this.name = 'ChatStreamError'
    this.savedMessage = savedMessage
  }
}

export function savedMessageFromChatStreamError(err: unknown): ChatMessage | null {
  return err instanceof ChatStreamError ? err.savedMessage || null : null
}

export async function listModels(apiKeyId: number): Promise<ChatModelListResponse> {
  const { data } = await apiClient.get<ChatModelListResponse>('/chat/models', {
    params: { api_key_id: apiKeyId }
  })
  return data
}

export async function exportConversations(): Promise<Blob> {
  return exportConversationsWithFetch(false)
}

async function exportConversationsWithFetch(retried: boolean): Promise<Blob> {
  const response = await fetch(buildApiUrl('/chat/export'), {
    method: 'GET',
    credentials: 'include',
    headers: exportRequestHeaders(),
  })

  if (response.status === 401 && !retried && await refreshAccessToken()) {
    return exportConversationsWithFetch(true)
  }
  if (!response.ok) {
    throw await exportRequestError(response)
  }
  return response.blob()
}

function exportRequestHeaders(): HeadersInit {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    'Accept-Language': getLocale(),
  }
  const token = localStorage.getItem('auth_token')
  if (token) headers.Authorization = `Bearer ${token}`
  return headers
}

async function refreshAccessToken(): Promise<boolean> {
  const refreshToken = localStorage.getItem('refresh_token')
  if (!refreshToken) return false

  try {
    const response = await fetch(buildApiUrl('/auth/refresh'), {
      method: 'POST',
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json',
        'Accept-Language': getLocale(),
      },
      body: JSON.stringify({ refresh_token: refreshToken }),
    })
    if (!response.ok) return false
    const payload = await response.json()
    const data = payload?.code === 0 ? payload.data : payload
    if (!data?.access_token || !data?.refresh_token || !data?.expires_in) return false
    localStorage.setItem('auth_token', data.access_token)
    localStorage.setItem('refresh_token', data.refresh_token)
    localStorage.setItem('token_expires_at', String(Date.now() + data.expires_in * 1000))
    return true
  } catch {
    return false
  }
}

async function exportRequestError(response: Response): Promise<Error> {
  const text = await response.text().catch(() => '')
  if (!text) return new Error(`Export failed with ${response.status}`)
  try {
    const payload = JSON.parse(text)
    return new Error(payload?.message || payload?.error || `Export failed with ${response.status}`)
  } catch {
    return new Error(text)
  }
}

export async function listConversations(page = 1, pageSize = 12): Promise<PaginatedResponse<ChatConversation>> {
  const { data } = await apiClient.get<PaginatedResponse<ChatConversation>>('/chat/conversations', {
    params: { page, page_size: pageSize }
  })
  return data
}

export async function createConversation(payload: CreateChatConversationRequest): Promise<ChatConversation> {
  const { data } = await apiClient.post<ChatConversation>('/chat/conversations', payload)
  return data
}

export async function getConversation(id: number): Promise<ChatConversation> {
  const { data } = await apiClient.get<ChatConversation>(`/chat/conversations/${id}`)
  return data
}

export async function updateConversation(id: number, payload: UpdateChatConversationRequest): Promise<ChatConversation> {
  const { data } = await apiClient.put<ChatConversation>(`/chat/conversations/${id}`, payload)
  return data
}

export async function deleteConversation(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/chat/conversations/${id}`)
  return data
}

export async function appendMessage(id: number, payload: CreateChatMessageRequest): Promise<ChatMessage> {
  const { data } = await apiClient.post<ChatMessage>(`/chat/conversations/${id}/messages`, payload)
  return data
}

export async function deleteMessage(conversationId: number, messageId: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/chat/conversations/${conversationId}/messages/${messageId}`)
  return data
}

export async function streamConversationMessage(options: {
  conversationId: number
  attachments?: ChatStreamAttachment[]
  signal?: AbortSignal
  onDelta?: (text: string) => void
}): Promise<ChatMessage> {
  return streamConversationMessageWithFetch(options, false)
}

async function streamConversationMessageWithFetch(options: {
  conversationId: number
  attachments?: ChatStreamAttachment[]
  signal?: AbortSignal
  onDelta?: (text: string) => void
}, retried: boolean): Promise<ChatMessage> {
  const response = await fetch(buildApiUrl(`/chat/conversations/${options.conversationId}/stream`), {
    method: 'POST',
    credentials: 'include',
    headers: streamRequestHeaders(),
    body: JSON.stringify({ attachments: options.attachments || [] }),
    signal: options.signal,
  })

  if (response.status === 401 && !retried && await refreshAccessToken()) {
    return streamConversationMessageWithFetch(options, true)
  }
  if (!response.ok) {
    throw await exportRequestError(response)
  }
  if (!response.body) {
    throw new Error('Stream response body is empty')
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let savedMessage: ChatMessage | null = null

  while (true) {
    const { value, done } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const lines = buffer.split(/\r?\n/)
    buffer = lines.pop() || ''
    for (const line of lines) processConversationStreamLine(line)
  }

  buffer += decoder.decode()
  if (buffer.trim()) processConversationStreamLine(buffer)
  if (savedMessage) return savedMessage
  throw new Error('Stream ended before a saved assistant message was received')

  function processConversationStreamLine(line: string) {
    const trimmed = line.trim()
    if (!trimmed.startsWith('data:')) return
    const payload = trimmed.slice(5).trim()
    if (!payload) return
    const event = JSON.parse(payload) as ChatStreamEvent
    if (event.type === 'delta' && event.content) {
      options.onDelta?.(event.content)
      return
    }
    if (event.type === 'done' && event.message) {
      savedMessage = event.message
      return
    }
    if (event.type === 'error') {
      throw new ChatStreamError(event.error || 'Chat stream failed', event.saved_message)
    }
  }
}

function streamRequestHeaders(): HeadersInit {
  return {
    ...exportRequestHeaders(),
    Accept: 'text/event-stream',
    'Content-Type': 'application/json',
  }
}

export const chatAPI = {
  listModels,
  exportConversations,
  listConversations,
  createConversation,
  getConversation,
  updateConversation,
  deleteConversation,
  appendMessage,
  deleteMessage,
  streamConversationMessage,
  savedMessageFromChatStreamError,
}

export default chatAPI
