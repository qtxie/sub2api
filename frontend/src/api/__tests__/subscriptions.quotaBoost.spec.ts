import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post, put } = vi.hoisted(() => ({
  post: vi.fn(),
  put: vi.fn()
}))

vi.mock('../client', () => ({
  apiClient: {
    post,
    put
  }
}))

import { activateQuotaBoost } from '@/api/subscriptions'
import { setQuotaBoostPolicy } from '@/api/admin/subscriptions'
import type { UserSubscription } from '@/types'

const subscription = { id: 17 } as UserSubscription

describe('subscription quota boost APIs', () => {
  beforeEach(() => {
    post.mockReset()
    put.mockReset()
  })

  it('activates the current user subscription boost', async () => {
    post.mockResolvedValue({ data: subscription })

    await expect(activateQuotaBoost(17)).resolves.toBe(subscription)
    expect(post).toHaveBeenCalledWith('/subscriptions/17/quota-boost', {})
  })

  it('updates the admin monthly boost policy', async () => {
    put.mockResolvedValue({ data: subscription })

    await expect(setQuotaBoostPolicy(17, 5)).resolves.toBe(subscription)
    expect(put).toHaveBeenCalledWith('/admin/subscriptions/17/quota-boost', {
      monthly_limit: 5
    })
  })
})
