import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import SubscriptionsView from '../SubscriptionsView.vue'
import type { UserSubscription } from '@/types'

const list = vi.hoisted(() => vi.fn())
const setQuotaBoostPolicy = vi.hoisted(() => vi.fn())
const getAllGroups = vi.hoisted(() => vi.fn())
const showSuccess = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())

vi.mock('@/api/admin', () => ({
  adminAPI: {
    subscriptions: {
      list,
      setQuotaBoostPolicy
    },
    groups: {
      getAll: getAllGroups
    },
    usage: {
      searchUsers: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('@/composables/usePersistedPageSize', () => ({
  getPersistedPageSize: () => 20
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const subscription = {
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
  weekly_window_start: null,
  monthly_window_start: null,
  effective_daily_limit_usd: 10,
  quota_boost: {
    monthly_limit: 2,
    used_this_month: 1,
    remaining_this_month: 1,
    active_today: false,
    available_today: true
  },
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  user: { id: 2, email: 'user@test.com' },
  group: {
    id: 3,
    name: 'Pro',
    platform: 'openai',
    subscription_type: 'subscription',
    status: 'active',
    daily_limit_usd: 10,
    weekly_limit_usd: null,
    monthly_limit_usd: null,
    rate_multiplier: 1,
    description: null
  }
} as UserSubscription

describe('admin subscription quota boost policy', () => {
  beforeEach(() => {
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => null),
      setItem: vi.fn(),
      removeItem: vi.fn()
    })
    list.mockReset()
    setQuotaBoostPolicy.mockReset()
    getAllGroups.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    list.mockResolvedValue({ items: [subscription], total: 1, pages: 1 })
    getAllGroups.mockResolvedValue([subscription.group])
    setQuotaBoostPolicy.mockResolvedValue(subscription)
  })

  it('opens the policy modal with the current limit and saves a new value', async () => {
    const wrapper = mount(SubscriptionsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
          DataTable: {
            props: ['data'],
            template: '<div><slot v-if="data[0]" name="cell-actions" :row="data[0]" /></div>'
          },
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>'
          },
          ConfirmDialog: true,
          Pagination: true,
          EmptyState: true,
          Select: true,
          GroupBadge: true,
          GroupOptionItem: true,
          Icon: { template: '<i />' },
          RouterLink: true
        }
      }
    })
    await flushPromises()

    await wrapper.find('button[title="admin.subscriptions.quotaBoostPolicy"]').trigger('click')
    const input = wrapper.find('input[type="number"]')
    expect((input.element as HTMLInputElement).value).toBe('2')

    await input.setValue('5')
    await wrapper.find('form#quota-boost-policy-form').trigger('submit')
    await flushPromises()

    expect(setQuotaBoostPolicy).toHaveBeenCalledWith(17, 5)
    expect(showSuccess).toHaveBeenCalledWith('admin.subscriptions.quotaBoostPolicySaved')
  })
})
