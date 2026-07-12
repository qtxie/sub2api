import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { ApiKey } from '@/types'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  auth: { isAuthenticated: true }
}))

vi.mock('@/api/keys', () => ({
  keysAPI: { list: mocks.list }
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => mocks.auth
}))

function makeKey(
  platform: string,
  { status = 'active', allowed = true }: { status?: string; allowed?: boolean } = {}
): ApiKey {
  return {
    id: 1,
    status,
    group: { platform, allow_image_generation: allowed }
  } as ApiKey
}

function page(items: ApiKey[], pages = 1) {
  return { items, page: 1, page_size: 100, total: items.length, pages }
}

async function loadSubject() {
  return import('@/composables/useImageGenerationAccess')
}

describe('useImageGenerationAccess', () => {
  beforeEach(() => {
    vi.resetModules()
    mocks.list.mockReset()
    mocks.auth.isAuthenticated = true
  })

  it('denies unauthenticated users without loading keys', async () => {
    mocks.auth.isAuthenticated = false
    const { useImageGenerationAccess } = await loadSubject()
    const access = useImageGenerationAccess()

    await expect(access.refreshImageGenerationAccess(true)).resolves.toBe(false)

    expect(mocks.list).not.toHaveBeenCalled()
    expect(access.canUseImageGeneration.value).toBe(false)
    expect(access.imageGenerationAccessLoaded.value).toBe(true)
  })

  it.each(['openai', 'gemini', 'antigravity'])(
    'allows active image-enabled %s keys',
    async (platform) => {
      mocks.list.mockResolvedValue(page([makeKey(platform)]))
      const { useImageGenerationAccess } = await loadSubject()
      const access = useImageGenerationAccess()

      await expect(access.refreshImageGenerationAccess(true)).resolves.toBe(true)
      expect(access.canUseImageGeneration.value).toBe(true)
    }
  )

  it('ignores inactive, disabled, and unsupported keys', async () => {
    mocks.list.mockResolvedValue(page([
      makeKey('openai', { status: 'disabled' }),
      makeKey('gemini', { allowed: false }),
      makeKey('grok')
    ]))
    const { useImageGenerationAccess } = await loadSubject()
    const access = useImageGenerationAccess()

    await expect(access.refreshImageGenerationAccess(true)).resolves.toBe(false)
    expect(access.canUseImageGeneration.value).toBe(false)
  })

  it('continues through pages until an eligible key is found', async () => {
    mocks.list
      .mockResolvedValueOnce(page([makeKey('openai', { allowed: false })], 2))
      .mockResolvedValueOnce(page([makeKey('gemini')], 2))
    const { useImageGenerationAccess } = await loadSubject()
    const access = useImageGenerationAccess()

    await expect(access.refreshImageGenerationAccess(true)).resolves.toBe(true)

    expect(mocks.list).toHaveBeenNthCalledWith(1, 1, 100, { status: 'active' })
    expect(mocks.list).toHaveBeenNthCalledWith(2, 2, 100, { status: 'active' })
  })

  it('coalesces concurrent loads and reuses the cached result', async () => {
    let resolveList!: (value: ReturnType<typeof page>) => void
    mocks.list.mockReturnValue(new Promise((resolve) => {
      resolveList = resolve
    }))
    const { useImageGenerationAccess } = await loadSubject()
    const access = useImageGenerationAccess()

    const first = access.refreshImageGenerationAccess(true)
    const second = access.refreshImageGenerationAccess()
    expect(access.imageGenerationAccessLoading.value).toBe(true)
    expect(mocks.list).toHaveBeenCalledTimes(1)

    resolveList(page([makeKey('openai')]))
    await expect(Promise.all([first, second])).resolves.toEqual([true, true])
    await expect(access.refreshImageGenerationAccess()).resolves.toBe(true)

    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(access.imageGenerationAccessLoading.value).toBe(false)
  })

  it('fails closed on API errors and can recover on a forced refresh', async () => {
    mocks.list.mockRejectedValueOnce(new Error('network unavailable'))
    const { useImageGenerationAccess } = await loadSubject()
    const access = useImageGenerationAccess()

    await expect(access.refreshImageGenerationAccess(true)).resolves.toBe(false)
    expect(access.canUseImageGeneration.value).toBe(false)
    expect(access.imageGenerationAccessLoaded.value).toBe(true)
    expect(access.imageGenerationAccessLoading.value).toBe(false)

    mocks.list.mockResolvedValueOnce(page([makeKey('antigravity')]))
    await expect(access.refreshImageGenerationAccess(true)).resolves.toBe(true)
    expect(access.canUseImageGeneration.value).toBe(true)
  })
})
