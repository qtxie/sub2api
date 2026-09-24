import { enableAutoUnmount, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import SubscriptionProgressMini from '../SubscriptionProgressMini.vue'
import type { UserSubscription } from '@/types'

const storeState = vi.hoisted(() => ({
  activeSubscriptions: [] as any[],
  fetchActiveSubscriptions: vi.fn()
}))

vi.mock('@/stores', () => ({
  useSubscriptionStore: () => ({
    get activeSubscriptions() {
      return storeState.activeSubscriptions
    },
    get hasActiveSubscriptions() {
      return storeState.activeSubscriptions.length > 0
    },
    fetchActiveSubscriptions: storeState.fetchActiveSubscriptions
  })
}))

vi.mock('@/utils/featureFlags', () => ({
  FeatureFlags: { subscription: 'subscription' },
  isFeatureFlagEnabled: () => true
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

enableAutoUnmount(afterEach)

function subscriptionFixture(): UserSubscription {
  return {
    id: 17,
    user_id: 2,
    group_id: 3,
    status: 'active',
    starts_at: '2026-07-01T00:00:00Z',
    expires_at: '2026-08-01T00:00:00Z',
    daily_usage_usd: 15,
    weekly_usage_usd: 0,
    monthly_usage_usd: 0,
    daily_window_start: '2026-07-10T00:00:00Z',
    weekly_window_start: null,
    monthly_window_start: null,
    effective_daily_limit_usd: 20,
    quota_boost: {
      monthly_limit: 1,
      used_this_month: 1,
      remaining_this_month: 0,
      active_today: true,
      available_today: false
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
    } as UserSubscription['group']
  }
}

describe('SubscriptionProgressMini', () => {
  beforeEach(() => {
    storeState.activeSubscriptions = [subscriptionFixture()]
    storeState.fetchActiveSubscriptions.mockReset()
    storeState.fetchActiveSubscriptions.mockResolvedValue(storeState.activeSubscriptions)
  })

  it('uses the effective daily limit while a quota boost is active', async () => {
    const wrapper = mount(SubscriptionProgressMini, {
      global: {
        stubs: {
          Icon: { template: '<i />' },
          RouterLink: { template: '<a><slot /></a>' }
        }
      }
    })

    await wrapper.find('button').trigger('click')

    expect(wrapper.text()).toContain('$15.00/$20.00')
    expect(wrapper.find('.bg-orange-500').exists()).toBe(true)
    expect(wrapper.find('.bg-red-500').exists()).toBe(false)
  })
})

describe('subscription expiry calendar labels', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 8, 22, 12))
  })
  afterEach(() => vi.useRealTimers())

  it.each([
    [new Date(2026, 8, 22, 18), 'expiresToday'],
    [new Date(2026, 8, 23, 18), 'expiresTomorrow'],
    [new Date(2026, 8, 22, 12), 'expired'],
    [new Date(2026, 8, 25, 12), 'daysRemaining'],
  ])('labels %s as %s', async (expires, label) => {
    storeState.activeSubscriptions = [{ id: 1, group_id: 1, expires_at: expires.toISOString(), group: { name: 'Plan' } }]
    const w = mount(SubscriptionProgressMini, { global: { stubs: { Icon: true, RouterLink: true } } })
    await w.get('button').trigger('click')
    expect(w.text()).toContain('subscriptionProgress.' + label)
  })
})
