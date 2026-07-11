import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ImageStudioView from '../ImageStudioView.vue'

const listKeys = vi.hoisted(() => vi.fn())
const generateImage = vi.hoisted(() => vi.fn())
const getImagePricing = vi.hoisted(() => vi.fn())
const listImageStudioGallery = vi.hoisted(() => vi.fn())
const saveImageStudioGalleryItem = vi.hoisted(() => vi.fn())
const showSuccess = vi.hoisted(() => vi.fn())
const showWarning = vi.hoisted(() => vi.fn())

vi.mock('@/api/keys', () => ({ keysAPI: { list: listKeys } }))
vi.mock('@/api/imagePlayground', () => ({ generateImage, getImagePricing }))
vi.mock('@/utils/imageStudioGallery', () => ({
  listImageStudioGallery,
  saveImageStudioGalleryItem,
  deleteImageStudioGalleryItem: vi.fn(),
  clearImageStudioGallery: vi.fn()
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showWarning, showError: vi.fn() })
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 42 } })
}))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key, locale: { value: 'en-US' } })
  }
})

const activeOpenAIKey = {
  id: 7,
  name: 'OpenAI image key',
  status: 'active',
  group: { platform: 'openai', allow_image_generation: true }
}
const activeGrokKey = {
  id: 8,
  name: 'Grok image key',
  status: 'active',
  group: { platform: 'grok', allow_image_generation: true }
}
const activeGeminiKey = {
  id: 9,
  name: 'Gemini image key',
  status: 'active',
  group: { platform: 'gemini', allow_image_generation: true }
}

function mountView() {
  return mount(ImageStudioView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: { template: '<i />' }
      }
    }
  })
}

describe('ImageStudioView', () => {
  beforeEach(() => {
    localStorage.clear()
    listKeys.mockReset().mockResolvedValue({ items: [activeOpenAIKey, activeGrokKey, activeGeminiKey], pages: 1 })
    listImageStudioGallery.mockReset().mockResolvedValue([])
    generateImage.mockReset().mockResolvedValue({ data: [{ b64_json: 'abc' }] })
    getImagePricing.mockReset().mockResolvedValue({
      currency: 'USD',
      pricing_kind: 'fixed',
      prices: [
        { size: '1024x1024', billing_tier: '1K', pricing_kind: 'fixed', unit_price: 0.1 },
        { size: '1536x1024', billing_tier: '2K', pricing_kind: 'fixed', unit_price: 0.15 },
        { size: '1024x1536', billing_tier: '2K', pricing_kind: 'fixed', unit_price: 0.15 },
        { size: '3840x2160', billing_tier: '4K', pricing_kind: 'fixed', unit_price: 0.3 },
        { size: '2160x3840', billing_tier: '4K', pricing_kind: 'fixed', unit_price: 0.3 }
      ]
    })
    saveImageStudioGalleryItem.mockReset().mockRejectedValue(new Error('quota exceeded'))
    showSuccess.mockReset()
    showWarning.mockReset()
    vi.stubGlobal('crypto', { randomUUID: () => 'generated-image' })
  })

  it('uses the shared app layout spacing without an extra page inset', () => {
    const wrapper = mountView()
    const page = wrapper.get('.w-full.space-y-6')

    expect(page.classes()).not.toEqual(expect.arrayContaining(['px-4', 'py-6', 'sm:px-6', 'max-w-7xl']))
    expect(wrapper.find('h1').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('imageStudio.description')
  })

  it('presents OpenAI and Gemini image-enabled keys', async () => {
    const wrapper = mountView()
    await flushPromises()

    const options = wrapper.find('#image-studio-key').findAll('option').map((option) => option.text())
    expect(options).toContain('OpenAI image key')
    expect(options).toContain('Gemini image key')
    expect(options).not.toContain('Grok image key')
  })

  it('loads and displays the selected key price once per billing tier', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(getImagePricing).toHaveBeenCalledWith({ api_key_id: 7 }, expect.any(AbortSignal))
    const pricing = wrapper.get('[data-testid="image-pricing"]')
    expect(pricing.text()).toContain('imageStudio.pricePerImage')
    expect(pricing.text()).toContain('1K')
    expect(pricing.text()).toContain('2K')
    expect(pricing.text()).toContain('4K')
    expect(pricing.text()).not.toContain('1024 x 1024')
    expect(pricing.text()).not.toContain('3840 x 2160')
    expect(pricing.text()).toContain('$0.10 imageStudio.perImage')
    expect(pricing.text().match(/\$0\.15 imageStudio\.perImage/g)).toHaveLength(1)
    expect(pricing.text().match(/\$0\.30 imageStudio\.perImage/g)).toHaveLength(1)
  })

  it('offers 4K sizes only for OpenAI keys', async () => {
    const wrapper = mountView()
    await flushPromises()

    const sizeSelect = wrapper.get('#image-studio-size')
    expect(sizeSelect.findAll('option').map((option) => option.attributes('value'))).toEqual([
      '1024x1024', '1536x1024', '1024x1536', '3840x2160', '2160x3840'
    ])
    await sizeSelect.setValue('3840x2160')
    await wrapper.get('#image-studio-key').setValue('9')
    await flushPromises()

    expect(sizeSelect.findAll('option').map((option) => option.attributes('value'))).toEqual([
      '1024x1024', '1536x1024', '1024x1536'
    ])
    expect((sizeSelect.element as HTMLSelectElement).value).toBe('1024x1024')
  })

  it('refreshes pricing when the selected Gemini model changes', async () => {
    const wrapper = mountView()
    await flushPromises()
    getImagePricing.mockClear()

    await wrapper.get('#image-studio-key').setValue('9')
    await flushPromises()
    expect(getImagePricing).toHaveBeenLastCalledWith({
      api_key_id: 9,
      model: 'gemini-3.1-flash-image'
    }, expect.any(AbortSignal))

    await wrapper.get('#image-studio-model').setValue('gemini-2.5-flash-image')
    await flushPromises()
    expect(getImagePricing).toHaveBeenLastCalledWith({
      api_key_id: 9,
      model: 'gemini-2.5-flash-image'
    }, expect.any(AbortSignal))
  })

  it('shows usage-based pricing without presenting a false fixed amount', async () => {
    getImagePricing.mockResolvedValueOnce({
      currency: 'USD',
      pricing_kind: 'usage_based',
      prices: [
        { size: '1024x1024', billing_tier: '1K', pricing_kind: 'usage_based', unit_price: null }
      ]
    })
    const wrapper = mountView()
    await flushPromises()

    const pricing = wrapper.get('[data-testid="image-pricing"]')
    expect(pricing.text()).toContain('imageStudio.usageBasedPricing')
    expect(pricing.text()).not.toContain('$0.00')
  })

  it('does not let a stale key price overwrite the latest selection', async () => {
    let resolveFirst!: (value: any) => void
    getImagePricing
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve }))
      .mockResolvedValueOnce({
        currency: 'USD',
        pricing_kind: 'fixed',
        prices: [{ size: '1024x1024', billing_tier: '2K', pricing_kind: 'fixed', unit_price: 0.2 }]
      })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('#image-studio-key').setValue('9')
    await flushPromises()
    expect(wrapper.get('[data-testid="image-pricing"]').text()).toContain('$0.20 imageStudio.perImage')

    resolveFirst({
      currency: 'USD',
      pricing_kind: 'fixed',
      prices: [{ size: '1024x1024', billing_tier: '1K', pricing_kind: 'fixed', unit_price: 0.99 }]
    })
    await flushPromises()
    expect(wrapper.get('[data-testid="image-pricing"]').text()).not.toContain('$0.99')
    expect(wrapper.get('[data-testid="image-pricing"]').text()).toContain('$0.20 imageStudio.perImage')
  })

  it('shows pricing failures without disabling image generation', async () => {
    getImagePricing.mockRejectedValueOnce(new Error('pricing offline'))
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="image-pricing"]').text()).toContain('pricing offline')
    await wrapper.get('#image-studio-prompt').setValue('paper kite')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
  })

  it('uses the selected Gemini image model and returned MIME type', async () => {
    generateImage.mockResolvedValueOnce({ data: [{ b64_json: 'webp', mime_type: 'image/webp' }] })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('#image-studio-key').setValue('9')
    await wrapper.get('#image-studio-model').setValue('gemini-2.5-flash-image')
    await wrapper.get('#image-studio-prompt').setValue('paper kite')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(generateImage).toHaveBeenCalledWith(expect.objectContaining({
      api_key_id: 9,
      model: 'gemini-2.5-flash-image'
    }), expect.any(AbortSignal))
    expect(wrapper.find('img').attributes('src')).toBe('data:image/webp;base64,webp')
    expect(saveImageStudioGalleryItem.mock.calls[0][0]).toEqual(expect.objectContaining({
      model: 'gemini-2.5-flash-image',
      outputFormat: 'webp'
    }))
  })

  it('only presents backgrounds supported by GPT Image 2', async () => {
    const wrapper = mountView()
    await flushPromises()

    const options = wrapper.get('#image-studio-background').findAll('option').map((option) => option.attributes('value'))
    expect(options).toEqual(['auto', 'opaque'])
    expect(options).not.toContain('transparent')
  })

  it('reports an error when generation returns no usable image', async () => {
    generateImage.mockResolvedValueOnce({ data: [] })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('#image-studio-prompt').setValue('paper kite')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('imageStudio.noImagesReturned')
    expect(wrapper.get('[data-testid="generation-placeholder"]').text()).toContain('imageStudio.generationFailed')
    expect(wrapper.get('[data-testid="generation-placeholder"] button').text()).toContain('imageStudio.retryGeneration')
    expect(showSuccess).not.toHaveBeenCalled()
  })

  it('shows one animated placeholder per requested image until results arrive', async () => {
    let resolveGeneration!: (value: { data: Array<{ b64_json: string }> }) => void
    generateImage.mockImplementationOnce(() => new Promise((resolve) => { resolveGeneration = resolve }))
    const randomUUID = vi.fn()
      .mockReturnValueOnce('generated-image-1')
      .mockReturnValueOnce('generated-image-2')
    vi.stubGlobal('crypto', { randomUUID })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('#image-studio-prompt').setValue('two paper kites')
    await wrapper.find('input[type="number"]').setValue(2)
    await wrapper.find('form').trigger('submit')

    const placeholders = wrapper.findAll('[data-testid="generation-placeholder"]')
    expect(placeholders).toHaveLength(2)
    expect(placeholders[0].text()).toContain('imageStudio.generatingImage')
    expect(placeholders[0].text()).toContain('imageStudio.elapsedSeconds')

    resolveGeneration({ data: [{ b64_json: 'first' }, { b64_json: 'second' }] })
    await flushPromises()

    expect(wrapper.findAll('[data-testid="generation-placeholder"]')).toHaveLength(0)
    expect(wrapper.findAll('.image-studio-result-reveal')).toHaveLength(2)
  })

  it('removes generation placeholders when the request is cancelled', async () => {
    generateImage.mockImplementationOnce((_payload: unknown, signal: AbortSignal) => new Promise((_resolve, reject) => {
      signal.addEventListener('abort', () => reject(Object.assign(new Error('cancelled'), { code: 'ERR_CANCELED' })))
    }))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('#image-studio-prompt').setValue('paper kite')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.findAll('[data-testid="generation-placeholder"]')).toHaveLength(1)
    const cancelButton = wrapper.findAll('button').find((button) => button.text().includes('common.cancel'))
    expect(cancelButton).toBeDefined()
    await cancelButton!.trigger('click')
    await flushPromises()

    expect(wrapper.findAll('[data-testid="generation-placeholder"]')).toHaveLength(0)
    expect(wrapper.text()).toContain('imageStudio.emptyGallery')
  })

  it('surfaces gallery loading failures', async () => {
    listImageStudioGallery.mockRejectedValueOnce(new Error('storage unavailable'))
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('storage unavailable')
  })

  it('keeps a completed generation visible when local history persistence fails', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('#image-studio-prompt').setValue('paper kite')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(wrapper.find('img').attributes('src')).toContain('data:image/png;base64,abc')
    expect(showSuccess).toHaveBeenCalledWith('imageStudio.generated')
    expect(showWarning).toHaveBeenCalledWith('imageStudio.gallerySaveFailed')
  })
})
