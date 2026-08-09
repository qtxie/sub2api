import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export interface SessionArchiveSummary {
  id: number
  user_id: number
  api_key_id?: number
  group_id?: number
  identity_kind: string
  protocol: string
  model: string
  turn_count: number
  request_count: number
  created_at: string
  updated_at: string
}

export interface SessionArchivePart {
  id: number
  ordinal: number
  kind: 'text' | 'image' | 'file'
  blob_id: number
  byte_length: number
  detected_mime: string
  declared_mime: string
  original_filename: string
  source_path: string
  text?: string
}

export interface SessionArchiveTurn {
  id: number
  ordinal: number
  role: string
  branch_key: string
  parts: SessionArchivePart[]
}

export interface SessionArchiveDetail extends SessionArchiveSummary {
  turns: SessionArchiveTurn[]
}

export async function list(params: { page?: number; page_size?: number; user_id?: number }): Promise<PaginatedResponse<SessionArchiveSummary>> {
  const { data } = await apiClient.get('/admin/user-sessions', { params })
  return data
}

export async function get(id: number): Promise<SessionArchiveDetail> {
  const { data } = await apiClient.get(`/admin/user-sessions/${id}`)
  return data
}

export async function downloadBlob(sessionId: number, blobId: number): Promise<Blob> {
  const { data } = await apiClient.get(`/admin/user-sessions/${sessionId}/blobs/${blobId}`, { responseType: 'blob' })
  return data
}

export async function exportSession(id: number): Promise<Blob> {
  const { data } = await apiClient.get(`/admin/user-sessions/${id}/export`, { responseType: 'blob' })
  return data
}

export async function deleteSession(id: number): Promise<{ deleted: boolean }> {
  const { data } = await apiClient.delete(`/admin/user-sessions/${id}`)
  return data
}

export default { list, get, downloadBlob, exportSession, deleteSession }
