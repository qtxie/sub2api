import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageStatsCards from '../UsageStatsCards.vue'

const messages: Record<string, string> = {
  'usage.totalRequests': 'Total Requests',
  'usage.inSelectedRange': 'in selected range',
  'usage.totalTokens': 'Total Tokens',
  'usage.in': 'In',
  'usage.out': 'Out',
  'usage.cacheTotal': 'Cache',
  'usage.cacheBreakdown': 'Cache Token Breakdown',
  'usage.cacheCreationTokensLabel': 'Cache Creation',
  'usage.cacheReadTokensLabel': 'Cache Read',
  'usage.cacheHitRate': 'Cache hit rate',
  'usage.cacheHitRateHint': 'Cache reads divided by all prompt-side tokens.',
  'usage.totalCost': 'Total Cost',
  'usage.accountCost': 'Cost',
  'usage.standardCost': 'Standard',
  'usage.avgDuration': 'Avg Duration',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const stats = {
  total_requests: 1,
  total_input_tokens: 100,
  total_output_tokens: 50,
  total_cache_tokens: 34,
  total_cache_creation_tokens: 12,
  total_cache_read_tokens: 22,
  total_tokens: 184,
  total_cost: 0.001,
  total_actual_cost: 0.001,
  total_account_cost: 0.001,
  average_duration_ms: 250,
}

describe('UsageStatsCards', () => {
  it('shows cache token breakdown values', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('Cache: 34')
    expect(text).toContain('Cache Token Breakdown')
    expect(text).toContain('Cache Creation')
    expect(text).toContain('12')
    expect(text).toContain('Cache Read')
    expect(text).toContain('22')
    expect(text).toContain('Cache hit rate: 16.4%')
    expect(wrapper.get('[title="Cache reads divided by all prompt-side tokens."]').exists()).toBe(true)
  })

  it('shows a zero cache hit rate when there are no prompt-side tokens', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats: {
          ...stats,
          total_input_tokens: 0,
          total_cache_tokens: 0,
          total_cache_creation_tokens: 0,
          total_cache_read_tokens: 0,
          total_tokens: stats.total_output_tokens,
        },
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    expect(wrapper.text()).toContain('Cache hit rate: 0.0%')
  })
})
