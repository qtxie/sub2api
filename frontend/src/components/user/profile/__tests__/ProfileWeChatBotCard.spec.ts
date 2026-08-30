import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ProfileWeChatBotCard from '../ProfileWeChatBotCard.vue'

const {
  getUserStatus,
  createLoginQR,
  pollLoginQR,
  disconnect,
  updateUserSettings,
  sendTestNotification,
  listKeys,
  toDataURL,
  showSuccess,
  showError,
} = vi.hoisted(() => ({
  getUserStatus: vi.fn(),
  createLoginQR: vi.fn(),
  pollLoginQR: vi.fn(),
  disconnect: vi.fn(),
  updateUserSettings: vi.fn(),
  sendTestNotification: vi.fn(),
  listKeys: vi.fn(),
  toDataURL: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api', () => ({
  keysAPI: { list: listKeys },
  weChatBotAPI: {
    getUserStatus,
    createLoginQR,
    pollLoginQR,
    disconnect,
    updateUserSettings,
    sendTestNotification,
  },
}))

vi.mock('qrcode', () => ({
  default: { toDataURL }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    locale: { value: 'en' },
    t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key,
  })
}))

describe('ProfileWeChatBotCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getUserStatus.mockResolvedValue({
      available: true,
      logged_in: false,
      enabled: true,
      notify_admin: true,
      notify_balance: true,
      notify_login: true,
      chat_enabled: false,
      chat_model: 'gpt-4o-mini',
      delivery_open: false,
      outbound_count: 0,
    })
    listKeys.mockResolvedValue({ items: [] })
    createLoginQR.mockResolvedValue({
      login_id: 'login-1',
      qrcode: 'qr-id',
      qrcode_img_content: 'https://weixin.qq.com/personal-qr',
      expires_at: '2026-08-30T14:00:00Z',
    })
    toDataURL.mockResolvedValue('data:image/png;base64,qr')
  })

  function mountCard() {
    return mount(ProfileWeChatBotCard, {
      global: {
        stubs: {
          Icon: true,
          ConfirmDialog: true,
          Toggle: {
            props: ['modelValue'],
            template: '<div />',
          },
        },
      },
    })
  }

  it('creates a personal login QR from the user profile', async () => {
    const wrapper = mountCard()
    await flushPromises()

    await wrapper.get('[data-testid="wechat-bot-connect"]').trigger('click')
    await flushPromises()

    expect(createLoginQR).toHaveBeenCalledOnce()
    expect(toDataURL).toHaveBeenCalledWith('https://weixin.qq.com/personal-qr', {
      width: 224,
      margin: 1,
    })
    expect(wrapper.get('img').attributes('src')).toBe('data:image/png;base64,qr')
    wrapper.unmount()
  })
})
