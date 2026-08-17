import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ImageStudioView from '../ImageStudioView.vue'

const listKeys = vi.hoisted(() => vi.fn())
const getPricing = vi.hoisted(() => vi.fn())
const getCapabilities = vi.hoisted(() => vi.fn())
const generateImage = vi.hoisted(() => vi.fn())
const listGallery = vi.hoisted(() => vi.fn())

vi.mock('@/api/keys', () => ({ keysAPI: { list: listKeys } }))
vi.mock('@/api/imageStudio', () => ({
  generateImage,
  getImageStudioCapabilities: getCapabilities,
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
    generateImage.mockReset().mockResolvedValue({ data: [{ b64_json: 'aGVsbG8=', mime_type: 'image/png' }] })
    getCapabilities.mockReset().mockImplementation(async (apiKeyId: number) => {
      if (apiKeyId === 8) {
        return {
          provider: 'gemini',
          default_model: 'gemini-3.1-flash-image',
          models: [
            {
              id: 'gemini-3.1-flash-image', label: 'Gemini 3.1 Flash Image',
              aspect_ratios: ['1:1', '16:9'], image_sizes: ['1K', '2K'], resolutions: [], qualities: [],
              backgrounds: [], output_formats: [], max_images: 1, supports_custom_size: false
            },
            {
              id: 'gemini-2.5-flash-image', label: 'Gemini 2.5 Flash Image',
              aspect_ratios: ['1:1', '16:9'], image_sizes: [], resolutions: [], qualities: [],
              backgrounds: [], output_formats: [], max_images: 1, supports_custom_size: false
            }
          ]
        }
      }
      if (apiKeyId === 10) {
        return {
          provider: 'grok',
          default_model: 'grok-imagine-image-2.0',
          models: [{
            id: 'grok-imagine-image-2.0', label: 'Grok Imagine Image 2.0',
            aspect_ratios: ['auto', '1:1', '20:9'], image_sizes: [], resolutions: ['1k', '2k'],
            qualities: ['medium', 'low'], backgrounds: [], output_formats: [], max_images: 10, supports_custom_size: false
          }]
        }
      }
      return {
        provider: 'openai', default_model: 'gpt-image-2',
        models: [{
          id: 'gpt-image-2', label: 'GPT Image 2', aspect_ratios: [], image_sizes: [
            '1024x1024', '1536x1024', '1024x1536', '2048x2048', '2048x1152', '1152x2048', '3840x2160', '2160x3840'
          ], resolutions: [], qualities: ['auto', 'low', 'medium', 'high'], backgrounds: ['auto', 'opaque'],
          output_formats: ['png', 'jpeg', 'webp'], max_images: 4, supports_custom_size: true
        }]
      }
    })
    getPricing.mockReset().mockResolvedValue({
      currency: 'USD', pricing_kind: 'fixed',
      prices: [{ size: '1024x1024', billing_tier: '1K', pricing_kind: 'fixed', unit_price: 0.1 }]
    })
    listKeys.mockReset().mockResolvedValue({
      pages: 1,
      items: [
         { id: 7, name: 'OpenAI image', status: 'active', group: { platform: 'openai', allow_image_generation: true } },
         { id: 8, name: 'Gemini image', status: 'active', group: { platform: 'gemini', allow_image_generation: true } },
         { id: 10, name: 'Grok image', status: 'active', group: { platform: 'grok', allow_image_generation: true } },
        { id: 9, name: 'OpenAI disabled', status: 'active', group: { platform: 'openai', allow_image_generation: false } }
      ]
    })
  })

  it('shows all eligible provider keys and provides a large prompt editor', async () => {
    const wrapper = mountView()
    await flushPromises()

    const keyOptions = wrapper.find('#image-studio-key').findAll('option').map((option) => option.text())
    expect(keyOptions).toContain('OpenAI image')
    expect(keyOptions).toContain('Gemini image')
    expect(keyOptions).toContain('Grok image')
    expect(keyOptions).not.toContain('OpenAI disabled')
    expect(wrapper.get('#image-studio-prompt').attributes('rows')).toBe('10')
    expect(getCapabilities).toHaveBeenCalledWith(7, expect.any(AbortSignal))
    expect(getPricing).toHaveBeenCalledWith(7, 'gpt-image-2', expect.any(AbortSignal))
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

  it('renders only Gemini-supported controls and sends an Interactions payload', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('#image-studio-key').setValue('8')
    await flushPromises()

    expect(wrapper.get('#image-studio-model').findAll('option').map((option) => option.attributes('value')))
      .toEqual(['gemini-3.1-flash-image', 'gemini-2.5-flash-image'])
    expect(wrapper.get('#image-studio-aspect-ratio').exists()).toBe(true)
    expect(wrapper.get('#image-studio-image-size').exists()).toBe(true)
    expect(wrapper.find('#image-studio-resolution').exists()).toBe(false)
    expect(wrapper.find('.background-control').exists()).toBe(false)
    expect(wrapper.find('.segmented-control.compact').exists()).toBe(false)
    expect(wrapper.find('.count-control').exists()).toBe(false)

    await wrapper.get('#image-studio-prompt').setValue('a glass lighthouse')
    await wrapper.get('#image-studio-aspect-ratio').setValue('16:9')
    await wrapper.get('#image-studio-image-size').setValue('2K')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(generateImage).toHaveBeenCalledWith({
      api_key_id: 8,
      prompt: 'a glass lighthouse',
      model: 'gemini-3.1-flash-image',
      aspect_ratio: '16:9',
      image_size: '2K'
    }, expect.any(AbortSignal))
  })

  it('omits image size for Gemini models that do not advertise it', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('#image-studio-key').setValue('8')
    await flushPromises()
    await wrapper.get('#image-studio-model').setValue('gemini-2.5-flash-image')
    await flushPromises()

    expect(wrapper.find('#image-studio-image-size').exists()).toBe(false)
    await wrapper.get('#image-studio-prompt').setValue('a moonlit lake')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(generateImage).toHaveBeenCalledWith({
      api_key_id: 8,
      prompt: 'a moonlit lake',
      model: 'gemini-2.5-flash-image',
      aspect_ratio: '1:1'
    }, expect.any(AbortSignal))
  })

  it('renders only Grok-supported controls and sends documented image options', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('#image-studio-key').setValue('10')
    await flushPromises()

    expect(wrapper.find('#image-studio-size').exists()).toBe(false)
    expect(wrapper.get('#image-studio-aspect-ratio').exists()).toBe(true)
    expect(wrapper.get('#image-studio-resolution').exists()).toBe(true)
    expect(wrapper.find('.background-control').exists()).toBe(false)
    expect(wrapper.find('.segmented-control.compact').exists()).toBe(false)
    expect(wrapper.get('.count-control').exists()).toBe(true)

    await wrapper.get('#image-studio-prompt').setValue('a red observatory')
    await wrapper.get('#image-studio-aspect-ratio').setValue('20:9')
    await wrapper.get('#image-studio-resolution').setValue('2k')
    await wrapper.findAll('.segmented-control.two-columns button')[1].trigger('click')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(generateImage).toHaveBeenCalledWith({
      api_key_id: 10,
      prompt: 'a red observatory',
      model: 'grok-imagine-image-2.0',
      aspect_ratio: '20:9',
      resolution: '2k',
      quality: 'low',
      n: 1
    }, expect.any(AbortSignal))
  })
})
