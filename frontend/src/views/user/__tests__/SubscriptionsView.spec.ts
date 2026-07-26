import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import SubscriptionsView from '../SubscriptionsView.vue'
import type { UserSubscription } from '@/types'

const getMySubscriptions = vi.hoisted(() => vi.fn())
const activateQuotaBoost = vi.hoisted(() => vi.fn())
const showSuccess = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())

vi.mock('@/api/subscriptions', () => ({
  default: {
    getMySubscriptions,
    activateQuotaBoost
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess,
    showError,
    cachedPublicSettings: { server_utc_offset: '+08:00' }
  })
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

function subscriptionFixture(overrides: Partial<UserSubscription> = {}): UserSubscription {
  return {
    id: 17,
    user_id: 2,
    group_id: 3,
    status: 'active',
    starts_at: '2026-07-01T00:00:00Z',
    expires_at: '2026-08-01T00:00:00Z',
    daily_usage_usd: 2,
    weekly_usage_usd: 2,
    monthly_usage_usd: 2,
    daily_window_start: '2026-07-10T00:00:00Z',
    weekly_window_start: '2026-07-07T00:00:00Z',
    monthly_window_start: '2026-07-01T00:00:00Z',
    effective_daily_limit_usd: 10,
    quota_boost: {
      monthly_limit: 3,
      used_this_month: 0,
      remaining_this_month: 3,
      active_today: false,
      available_today: true,
      day_resets_at: '2026-07-11T00:00:00Z',
      month_resets_at: '2026-08-01T00:00:00Z'
    },
    created_at: '2026-07-01T00:00:00Z',
    updated_at: '2026-07-01T00:00:00Z',
    group: {
      id: 3,
      name: 'Pro',
      platform: 'openai',
      daily_limit_usd: 10,
      weekly_limit_usd: null,
      monthly_limit_usd: null,
      rate_multiplier: 1
    } as UserSubscription['group'],
    ...overrides
  }
}

describe('user subscription daily quota boost', () => {
  beforeEach(() => {
    getMySubscriptions.mockReset()
    activateQuotaBoost.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
  })

  it('activates 2x quota and updates the displayed effective daily limit', async () => {
    const initial = subscriptionFixture()
    const boosted = subscriptionFixture({
      effective_daily_limit_usd: 20,
      quota_boost: {
        ...initial.quota_boost,
        used_this_month: 1,
        remaining_this_month: 2,
        active_today: true,
        available_today: false,
        activated_at: '2026-07-10T08:00:00Z'
      }
    })
    getMySubscriptions.mockResolvedValue([initial])
    activateQuotaBoost.mockResolvedValue(boosted)

    const wrapper = mount(SubscriptionsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Icon: { template: '<i />' }
        }
      }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('$2.00 / $10.00')
    const button = wrapper.find('button[title="userSubscriptions.quotaBoost"]')
    expect(button.attributes('disabled')).toBeUndefined()

    await button.trigger('click')
    await flushPromises()

    expect(activateQuotaBoost).toHaveBeenCalledWith(17)
    expect(wrapper.text()).toContain('$2.00 / $20.00')
    expect(wrapper.text()).toContain('userSubscriptions.quotaBoostActive')
    expect(showSuccess).toHaveBeenCalledWith('userSubscriptions.quotaBoostSuccess')
  })
})
