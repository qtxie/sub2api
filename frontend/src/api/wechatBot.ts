import { apiClient } from './client'

export interface WeChatBotUserStatus {
  available: boolean
  logged_in: boolean
  bot_id?: string
  ilink_user_id?: string
  enabled: boolean
  notify_admin: boolean
  notify_balance: boolean
  notify_login: boolean
  chat_enabled: boolean
  chat_api_key_id?: number
  chat_model: string
  last_inbound_at?: string
  last_connected_at?: string
  last_error?: string
  delivery_open: boolean
  outbound_count: number
}

export interface WeChatBotSettingsUpdate {
  enabled: boolean
  notify_admin: boolean
  notify_balance: boolean
  notify_login: boolean
  chat_enabled: boolean
  chat_api_key_id: number | null
  chat_model: string
}

export interface WeChatBotAdminStatus {
  running: boolean
  connected_users: number
  delivery_open_users: number
  pending_messages: number
  users_with_errors: number
}

export interface WeChatBotLoginQR {
  login_id: string
  qrcode: string
  qrcode_img_content?: string
  url?: string
  expires_at: string
}

export interface WeChatBotLoginStatus {
  status: 'wait' | 'scaned' | 'scaned_but_redirect' | 'confirmed' | 'expired' | string
  logged_in: boolean
  bot_id?: string
}

export async function getUserStatus(): Promise<WeChatBotUserStatus> {
  const { data } = await apiClient.get<WeChatBotUserStatus>('/user/wechat-bot')
  return data
}

export async function updateUserSettings(
  settings: WeChatBotSettingsUpdate
): Promise<WeChatBotUserStatus> {
  const { data } = await apiClient.put<WeChatBotUserStatus>('/user/wechat-bot/settings', settings)
  return data
}

export async function disconnect(): Promise<void> {
  await apiClient.delete('/user/wechat-bot')
}

export async function sendTestNotification(): Promise<void> {
  await apiClient.post('/user/wechat-bot/test')
}

export async function getAdminStatus(): Promise<WeChatBotAdminStatus> {
  const { data } = await apiClient.get<WeChatBotAdminStatus>('/admin/wechat-bot/status')
  return data
}

export async function createLoginQR(): Promise<WeChatBotLoginQR> {
  const { data } = await apiClient.post<WeChatBotLoginQR>('/user/wechat-bot/login/qr')
  return data
}

export async function pollLoginQR(loginID: string): Promise<WeChatBotLoginStatus> {
  const { data } = await apiClient.get<WeChatBotLoginStatus>(
    `/user/wechat-bot/login/${encodeURIComponent(loginID)}`
  )
  return data
}

export async function broadcast(payload: {
  title?: string
  message: string
  user_ids?: number[]
}): Promise<{ queued: number }> {
  const { data } = await apiClient.post<{ queued: number }>('/admin/wechat-bot/broadcast', payload)
  return data
}

export const weChatBotAPI = {
  getUserStatus,
  createLoginQR,
  pollLoginQR,
  updateUserSettings,
  disconnect,
  sendTestNotification,
  getAdminStatus,
  broadcast,
}

export default weChatBotAPI
