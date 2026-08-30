import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, put, remove } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  remove: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post, put, delete: remove }
}))

import { weChatBotAPI } from '@/api/wechatBot'

describe('wechat bot api', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    put.mockReset()
    remove.mockReset()
  })

  it('uses user-owned login and settings endpoints', async () => {
    get.mockResolvedValueOnce({ data: { available: true, logged_in: false } })
    post.mockResolvedValueOnce({ data: { login_id: 'login-1', qrcode: 'qr' } })
    get.mockResolvedValueOnce({ data: { status: 'wait', logged_in: false } })
    put.mockResolvedValueOnce({ data: { available: true, logged_in: true } })

    await weChatBotAPI.getUserStatus()
    await weChatBotAPI.createLoginQR()
    await weChatBotAPI.pollLoginQR('login/id with spaces')
    const settings = {
      enabled: true,
      notify_admin: true,
      notify_balance: false,
      notify_login: true,
      chat_enabled: true,
      chat_api_key_id: 19,
      chat_model: 'gpt-5.6-terra',
    }
    await weChatBotAPI.updateUserSettings(settings)

    expect(get).toHaveBeenNthCalledWith(1, '/user/wechat-bot')
    expect(post).toHaveBeenCalledWith('/user/wechat-bot/login/qr')
    expect(get).toHaveBeenNthCalledWith(2, '/user/wechat-bot/login/login%2Fid%20with%20spaces')
    expect(put).toHaveBeenCalledWith('/user/wechat-bot/settings', settings)
  })

  it('keeps administrator access limited to status and broadcasts', async () => {
    get.mockResolvedValueOnce({ data: { running: true, connected_users: 4 } })
    post.mockResolvedValueOnce({ data: { queued: 4 } })

    await weChatBotAPI.getAdminStatus()
    const result = await weChatBotAPI.broadcast({ title: 'Maintenance', message: 'Starting soon' })

    expect(get).toHaveBeenCalledWith('/admin/wechat-bot/status')
    expect(post).toHaveBeenCalledWith('/admin/wechat-bot/broadcast', {
      title: 'Maintenance',
      message: 'Starting soon',
    })
    expect(result.queued).toBe(4)
  })

  it('disconnects only the current user account', async () => {
    remove.mockResolvedValue({ data: { success: true } })

    await weChatBotAPI.disconnect()

    expect(remove).toHaveBeenCalledOnce()
    expect(remove).toHaveBeenCalledWith('/user/wechat-bot')
  })
})
