import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ImageStudioView from '../ImageStudioView.vue'

const listKeys = vi.hoisted(() => vi.fn())
const getPricing = vi.hoisted(() => vi.fn())
const listGallery = vi.hoisted(() => vi.fn())

vi.mock('@/api/keys', () => ({ keysAPI: { list: listKeys } }))
vi.mock('@/api/imageStudio', () => ({
  generateImage: vi.fn(),
  getImageStudioPricing: getPricing
}))
vi.mock('@/utils/imageStudioGallery', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/utils/imageStudioGallery')>()
  return {
    ...actual,
    listImageStudioGallery: listGallery,
    saveImageStudioGalleryItem: vi.fn(),
    deleteImageStudioGalleryItem: vi.fn(),
    clearImageStudioGallery: vi.fn()
  }
})
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: vi.fn(), showWarning: vi.fn(), showError: vi.fn() })
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 42 } }) }))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string) => key, locale: { value: 'en-US' } }) }
})

function mountView() {
  return mount(ImageStudioView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: { template: '<i />' },
        RouterLink: { template: '<a><slot /></a>' }
      }
    }
  })
}

describe('ImageStudioView', () => {
  beforeEach(() => {
    localStorage.clear()
    listGallery.mockReset().mockResolvedValue([])
    getPricing.mockReset().mockResolvedValue({
      currency: 'USD', pricing_kind: 'fixed',
      prices: [{ size: '1024x1024', billing_tier: '1K', pricing_kind: 'fixed', unit_price: 0.1 }]
    })
    listKeys.mockReset().mockResolvedValue({
      pages: 1,
      items: [
        { id: 7, name: 'OpenAI image', status: 'active', group: { platform: 'openai', allow_image_generation: true } },
        { id: 8, name: 'Gemini image', status: 'active', group: { platform: 'gemini', allow_image_generation: true } },
        { id: 9, name: 'OpenAI disabled', status: 'active', group: { platform: 'openai', allow_image_generation: false } }
      ]
    })
  })

  it('shows only eligible OpenAI keys and provides a large prompt editor', async () => {
    const wrapper = mountView()
    await flushPromises()

    const keyOptions = wrapper.find('#image-studio-key').findAll('option').map((option) => option.text())
    expect(keyOptions).toContain('OpenAI image')
    expect(keyOptions).not.toContain('Gemini image')
    expect(keyOptions).not.toContain('OpenAI disabled')
    expect(wrapper.get('#image-studio-prompt').attributes('rows')).toBe('10')
    expect(getPricing).toHaveBeenCalledWith(7, expect.any(AbortSignal))
  })

  it('offers automatic, popular, symmetric portrait, and custom sizes', async () => {
    const wrapper = mountView()
    await flushPromises()
    const sizeOptions = wrapper.get('#image-studio-size').findAll('option')
    expect(sizeOptions).toHaveLength(10)
    expect(sizeOptions.map((option) => option.attributes('value'))).toEqual([
      'auto',
      '1024x1024',
      '1536x1024',
      '1024x1536',
      '2048x2048',
      '2048x1152',
      '1152x2048',
      '3840x2160',
      '2160x3840',
      'custom'
    ])
    expect(wrapper.get<HTMLSelectElement>('#image-studio-size').element.value).toBe('auto')
    expect(sizeOptions.map((option) => option.text()).join(' ')).toContain('3840 x 2160 · 4K')
    expect(sizeOptions.map((option) => option.text()).join(' ')).toContain('2160 x 3840 · 4K')
    expect(wrapper.get('.studio-workspace').find('.studio-gallery').exists()).toBe(true)
  })

  it('validates custom dimensions and swaps their orientation', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('#image-studio-size').setValue('custom')

    const width = wrapper.get<HTMLInputElement>('#image-studio-width')
    const height = wrapper.get<HTMLInputElement>('#image-studio-height')
    await width.setValue('1537')
    expect(wrapper.get('[role="alert"]').text()).toBe('imageStudio.sizeErrorMultiple')

    await width.trigger('blur')
    expect(width.element.value).toBe('1536')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.get('[role="status"]').text()).toBe('imageStudio.sizeAdjusted')

    await width.setValue('2048')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await wrapper.get('.swap-size-button').trigger('click')
    expect(width.element.value).toBe('1024')
    expect(height.element.value).toBe('2048')
  })

  it('offers the three GPT Image 2 output formats without transparent background', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('.background-control button')).toHaveLength(2)
    expect(wrapper.find('.control-pair').exists()).toBe(false)
    expect(wrapper.get('.count-control').exists()).toBe(true)
    expect(wrapper.findAll('.four-columns button').map((button) => button.text())).toEqual([
      'imageStudio.qualityAuto',
      'imageStudio.qualityLow',
      'imageStudio.qualityMedium',
      'imageStudio.qualityHigh'
    ])
    expect(wrapper.findAll('.segmented-control.compact button').map((button) => button.text())).toEqual(['PNG', 'JPEG', 'WEBP'])
    expect(wrapper.text()).not.toContain('imageStudio.backgroundTransparent')
  })
})
