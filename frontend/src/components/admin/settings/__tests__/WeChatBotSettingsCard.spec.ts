import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import WeChatBotSettingsCard from '../WeChatBotSettingsCard.vue'

const { getAdminStatus, broadcast, showSuccess, showError } = vi.hoisted(() => ({
  getAdminStatus: vi.fn(),
  broadcast: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api', () => ({
  weChatBotAPI: { getAdminStatus, broadcast }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key,
  })
}))

describe('WeChatBotSettingsCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getAdminStatus.mockResolvedValue({
      running: true,
      connected_users: 3,
      delivery_open_users: 2,
      pending_messages: 4,
      users_with_errors: 1,
    })
    broadcast.mockResolvedValue({ queued: 3 })
  })

  function mountCard() {
    return mount(WeChatBotSettingsCard, {
      global: { stubs: { Icon: true } }
    })
  }

  it('shows aggregate user-owned bot status without an administrator login action', async () => {
    const wrapper = mountCard()
    await flushPromises()

    expect(getAdminStatus).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain('3')
    expect(wrapper.text()).toContain('4')
    expect(wrapper.find('[data-testid="wechat-bot-login"]').exists()).toBe(false)
  })

  it('queues an administrator broadcast for opted-in users', async () => {
    const wrapper = mountCard()
    await flushPromises()

    await wrapper.get('[data-testid="wechat-bot-broadcast-message"]').setValue('Maintenance starts soon')
    await wrapper.get('[data-testid="wechat-bot-broadcast"]').trigger('click')
    await flushPromises()

    expect(broadcast).toHaveBeenCalledWith({
      title: '',
      message: 'Maintenance starts soon',
    })
    expect(showSuccess).toHaveBeenCalled()
  })
})
