import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import VideoStudioView from '../VideoStudioView.vue'

const api = vi.hoisted(() => ({
  listKeys: vi.fn(),
  capabilities: vi.fn(),
  pricing: vi.fn(),
  generate: vi.fn(),
  status: vi.fn(),
  content: vi.fn()
}))
const galleryStore = vi.hoisted(() => ({
  list: vi.fn(),
  save: vi.fn(),
  remove: vi.fn(),
  clear: vi.fn()
}))
const polling = vi.hoisted(() => ({
  options: null as any,
  track: vi.fn(),
  pause: vi.fn(),
  resume: vi.fn(),
  stop: vi.fn(),
  stopAll: vi.fn(),
  isTracking: vi.fn(() => false)
}))

vi.mock('@/api/keys', () => ({ keysAPI: { list: api.listKeys } }))
vi.mock('@/api/videoStudio', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/api/videoStudio')>(),
  getVideoStudioCapabilities: api.capabilities,
  getVideoStudioPricing: api.pricing,
  generateVideo: api.generate,
  getVideoStatus: api.status,
  getVideoContent: api.content
}))
vi.mock('@/utils/videoStudioGallery', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/utils/videoStudioGallery')>(),
  listVideoStudioGallery: galleryStore.list,
  saveVideoStudioGalleryItem: galleryStore.save,
  deleteVideoStudioGalleryItem: galleryStore.remove,
  clearVideoStudioGallery: galleryStore.clear
}))
vi.mock('@/composables/useVideoStudioPolling', () => ({
  useVideoStudioPolling: (options: any) => {
    polling.options = options
    return polling
  }
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: vi.fn(), showWarning: vi.fn(), showError: vi.fn() })
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 42 } }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'en-US' } })
}))

function mountView() {
  return mount(VideoStudioView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: { template: '<i />' },
        RouterLink: { template: '<a><slot /></a>' }
      }
    }
  })
}

function pendingItem(createdAt = Date.now()) {
  return {
    id: '42:task-existing',
    requestId: 'task-existing',
    userId: 42,
    apiKeyId: 7,
    createdAt,
    updatedAt: createdAt,
    prompt: 'A quiet harbor',
    model: 'grok-imagine-video-1.5' as const,
    duration: 8,
    aspectRatio: '16:9' as const,
    resolution: '480p' as const,
    status: 'pending' as const,
    trackingPaused: false
  }
}

describe('VideoStudioView', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.clearAllMocks()
    polling.options = null
    galleryStore.list.mockResolvedValue([])
    galleryStore.save.mockImplementation(async (item) => item)
    galleryStore.remove.mockResolvedValue(undefined)
    galleryStore.clear.mockResolvedValue(undefined)
    api.capabilities.mockResolvedValue({
      model: 'grok-imagine-video-1.5',
      label: 'Grok Imagine Video 1.5',
      min_duration: 1,
      max_duration: 15,
      default_duration: 8,
      aspect_ratios: ['1:1', '16:9', '9:16', '4:3', '3:4', '3:2', '2:3'],
      resolutions: ['480p', '720p', '1080p']
    })
    api.pricing.mockResolvedValue({
      currency: 'USD', pricing_kind: 'fixed', duration: 8,
      prices: [
        { resolution: '480p', unit_price: 0.08, total_price: 0.64 },
        { resolution: '720p', unit_price: 0.14, total_price: 1.12 },
        { resolution: '1080p', unit_price: 0.25, total_price: 2 }
      ]
    })
    api.generate.mockResolvedValue({ request_id: 'task-new', status: 'pending' })
    api.listKeys.mockResolvedValue({
      pages: 1,
      items: [
        { id: 7, name: 'Grok video', status: 'active', group: { platform: 'grok', allow_image_generation: true } },
        { id: 8, name: 'Grok blocked', status: 'active', group: { platform: 'grok', allow_image_generation: false } },
        { id: 9, name: 'Grok inactive', status: 'disabled', group: { platform: 'grok', allow_image_generation: true } },
        { id: 10, name: 'Gemini key', status: 'active', group: { platform: 'gemini', allow_image_generation: true } }
      ]
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('shows only eligible Grok keys and submits exactly the documented fields', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('#video-studio-key').findAll('option').map((option) => option.text()))
      .toEqual(['videoStudio.selectApiKey', 'Grok video'])
    expect(api.capabilities).toHaveBeenCalledWith(7, expect.any(AbortSignal))
    expect(wrapper.get('#video-studio-aspect-ratio').findAll('option').map((option) => option.attributes('value')))
      .toEqual(['1:1', '16:9', '9:16', '4:3', '3:4', '3:2', '2:3'])
    expect(wrapper.findAll('.segmented-control button').map((button) => button.text()))
      .toEqual(['480P', '720P', '1080P'])
    expect(wrapper.find('.segmented-control button.active').text()).toBe('480P')
    expect(wrapper.find('#video-studio-model').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('audio')
    expect(wrapper.text()).not.toContain('seed')

    await wrapper.get('#video-studio-prompt').setValue('  A quiet city at dawn  ')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(api.generate).toHaveBeenCalledWith({
      api_key_id: 7,
      prompt: 'A quiet city at dawn',
      duration: 8,
      aspect_ratio: '16:9',
      resolution: '480p'
    }, expect.any(AbortSignal))
    expect(polling.track).toHaveBeenCalledWith({ requestId: 'task-new', apiKeyId: 7 })
  })

  it('shows sanitized terminal errors and expires a live task at the binding limit', async () => {
    const createdAt = Date.now()
    galleryStore.list.mockResolvedValue([pendingItem(createdAt)])
    const wrapper = mountView()
    await flushPromises()

    await polling.options.onResult(
      { requestId: 'task-existing', apiKeyId: 7 },
      { request_id: 'task-existing', status: 'failed', error: 'Moderation rejected the prompt' }
    )
    await flushPromises()
    expect(wrapper.text()).toContain('Moderation rejected the prompt')

    galleryStore.list.mockResolvedValue([pendingItem(createdAt)])
    wrapper.unmount()
    const second = mountView()
    await flushPromises()
    vi.spyOn(Date, 'now').mockReturnValue(createdAt + 24 * 60 * 60 * 1000)
    await polling.options.onError(
      { requestId: 'task-existing', apiKeyId: 7 },
      new Error('binding missing')
    )
    await flushPromises()
    expect(polling.stop).toHaveBeenCalledWith('task-existing')
    expect(second.text()).toContain('videoStudio.expired')
    expect(second.text()).not.toContain('binding missing')
    second.unmount()
  })
})
