import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ImageStudioView from '../ImageStudioView.vue'

const listKeys = vi.hoisted(() => vi.fn())
const getPricing = vi.hoisted(() => vi.fn())
const getCapabilities = vi.hoisted(() => vi.fn())
const generateImage = vi.hoisted(() => vi.fn())
const listGallery = vi.hoisted(() => vi.fn())
const saveGallery = vi.hoisted(() => vi.fn())
const deleteGallery = vi.hoisted(() => vi.fn())
const clearGallery = vi.hoisted(() => vi.fn())
const showSuccess = vi.hoisted(() => vi.fn())
const showWarning = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())

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
    saveImageStudioGalleryItem: saveGallery,
    deleteImageStudioGalleryItem: deleteGallery,
    clearImageStudioGallery: clearGallery
  }
})
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showWarning, showError })
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

async function selectSourceFiles(wrapper: ReturnType<typeof mountView>, files: File[]) {
  const input = wrapper.get<HTMLInputElement>('[data-testid="source-image-input"]')
  Object.defineProperty(input.element, 'files', { value: files, configurable: true })
  await input.trigger('change')
  await vi.waitFor(() => expect(input.element.disabled).toBe(false))
  await flushPromises()
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function generatedImage(base64: string) {
  return { data: [{ b64_json: base64, mime_type: 'image/png' }] }
}

describe('ImageStudioView', () => {
  beforeEach(() => {
    localStorage.clear()
    listGallery.mockReset().mockResolvedValue([])
    saveGallery.mockReset().mockResolvedValue(undefined)
    deleteGallery.mockReset().mockResolvedValue(undefined)
    clearGallery.mockReset().mockResolvedValue(undefined)
    showSuccess.mockReset()
    showWarning.mockReset()
    showError.mockReset()
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
              backgrounds: [], output_formats: [], max_images: 1, max_input_images: 3, supports_custom_size: false
            },
            {
              id: 'gemini-2.5-flash-image', label: 'Gemini 2.5 Flash Image',
              aspect_ratios: ['1:1', '16:9'], image_sizes: [], resolutions: [], qualities: [],
              backgrounds: [], output_formats: [], max_images: 1, max_input_images: 1, supports_custom_size: false
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
            qualities: ['medium', 'low'], backgrounds: [], output_formats: [], max_images: 10, max_input_images: 3, supports_custom_size: false
          }]
        }
      }
      return {
        provider: 'openai', default_model: 'gpt-image-2',
        models: [{
          id: 'gpt-image-2', label: 'GPT Image 2', aspect_ratios: [], image_sizes: [
            '1024x1024', '1536x1024', '1024x1536', '2048x2048', '2048x1152', '1152x2048', '3840x2160', '2160x3840'
          ], resolutions: [], qualities: ['auto', 'low', 'medium', 'high'], backgrounds: ['auto', 'opaque'],
          output_formats: ['png', 'jpeg', 'webp'], max_images: 4, max_input_images: 4, supports_custom_size: true
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

  it('validates, previews, and removes reference images', async () => {
    const wrapper = mountView()
    await flushPromises()
    const valid = new File([new Uint8Array([1, 2, 3])], 'valid.png', { type: 'image/png' })
    const unsupported = new File(['gif'], 'unsupported.gif', { type: 'image/gif' })
    const tooLarge = new File(['large'], 'large.webp', { type: 'image/webp' })
    Object.defineProperty(tooLarge, 'size', { value: 6 * 1024 * 1024 + 1 })

    await selectSourceFiles(wrapper, [valid, unsupported, tooLarge])

    expect(wrapper.findAll('.source-image-item')).toHaveLength(1)
    expect(wrapper.get('.source-image-preview-button img').attributes('src')).toBe('data:image/png;base64,AQID')
    expect(showError).toHaveBeenCalledWith('imageStudio.sourceImageFormatUnsupported')
    expect(showError).toHaveBeenCalledWith('imageStudio.sourceImageTooLarge')

    await wrapper.get('.source-image-preview-button').trigger('click')
    expect(wrapper.get('.preview-backdrop img').attributes('src')).toBe('data:image/png;base64,AQID')
    expect(wrapper.get('.preview-caption').text()).toContain('valid.png')
    await wrapper.get('.source-image-remove-button').trigger('click')
    expect(wrapper.find('.source-image-item').exists()).toBe(false)
    expect(wrapper.find('.preview-backdrop').exists()).toBe(false)

    const fiveMb = (name: string) => {
      const file = new File(['small payload'], name, { type: 'image/jpeg' })
      Object.defineProperty(file, 'size', { value: 5 * 1024 * 1024 })
      return file
    }
    await selectSourceFiles(wrapper, [fiveMb('one.jpg'), fiveMb('two.jpg'), fiveMb('three.jpg')])
    expect(wrapper.findAll('.source-image-item')).toHaveLength(2)
    expect(showError).toHaveBeenCalledWith('imageStudio.sourceImagesTotalTooLarge')
  })

  it('fills the available reference slots after skipping invalid files', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('#image-studio-key').setValue('10')
    await flushPromises()

    const input = wrapper.get<HTMLInputElement>('[data-testid="source-image-input"]')
    Object.defineProperty(input.element, 'files', { value: [
      new File(['bad'], 'bad.gif', { type: 'image/gif' }),
      new File(['one'], 'one.png', { type: 'image/png' }),
      new File(['two'], 'two.jpeg', { type: 'image/jpeg' }),
      new File(['three'], 'three.webp', { type: 'image/webp' })
    ], configurable: true })
    await input.trigger('change')
    await vi.waitFor(() => expect(wrapper.findAll('.source-image-item')).toHaveLength(3))
    await flushPromises()

    expect(wrapper.findAll('.source-image-item')).toHaveLength(3)
    expect(showError).toHaveBeenCalledWith('imageStudio.sourceImageFormatUnsupported')
    expect(showError).not.toHaveBeenCalledWith('imageStudio.sourceImageLimitReached')
  })

  it('deep-copies reference images into concurrent generation snapshots', async () => {
    const first = deferred<ReturnType<typeof generatedImage>>()
    const second = deferred<ReturnType<typeof generatedImage>>()
    generateImage.mockReset()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const wrapper = mountView()
    await flushPromises()

    await selectSourceFiles(wrapper, [new File(['first'], 'first.png', { type: 'image/png' })])
    await wrapper.get('#image-studio-prompt').setValue('first edit')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('.source-image-remove-button').trigger('click')
    await selectSourceFiles(wrapper, [new File(['second'], 'second.webp', { type: 'image/webp' })])
    await wrapper.get('#image-studio-prompt').setValue('second edit')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(generateImage).toHaveBeenCalledTimes(2)
    expect(generateImage.mock.calls[0][0]).toEqual(expect.objectContaining({
      prompt: 'first edit',
      source_images: [{ mime_type: 'image/png', data: 'Zmlyc3Q=' }]
    }))
    expect(generateImage.mock.calls[1][0]).toEqual(expect.objectContaining({
      prompt: 'second edit',
      source_images: [{ mime_type: 'image/webp', data: 'c2Vjb25k' }]
    }))
    expect(generateImage.mock.calls[0][0].source_images).not.toBe(generateImage.mock.calls[1][0].source_images)

    first.resolve(generatedImage('Zmlyc3Q='))
    second.resolve(generatedImage('c2Vjb25k'))
    await flushPromises()
  })

  it('sends only documented Grok edit options for one or multiple reference images', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('#image-studio-key').setValue('10')
    await flushPromises()

    await selectSourceFiles(wrapper, [new File(['first'], 'first.png', { type: 'image/png' })])
    expect(wrapper.find('#image-studio-aspect-ratio').exists()).toBe(false)
    expect(wrapper.find('#image-studio-resolution').exists()).toBe(false)
    expect(wrapper.find('.count-control').exists()).toBe(false)
    expect(wrapper.find('.segmented-control').exists()).toBe(false)
    await wrapper.get('#image-studio-prompt').setValue('restyle one image')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(generateImage).toHaveBeenNthCalledWith(1, {
      api_key_id: 10,
      prompt: 'restyle one image',
      model: 'grok-imagine-image-2.0',
      source_images: [{ mime_type: 'image/png', data: 'Zmlyc3Q=' }]
    }, expect.any(AbortSignal))
    const savedSingleEdit = saveGallery.mock.calls[0]?.[0]
    expect(savedSingleEdit).toEqual(expect.objectContaining({
      provider: 'grok',
      size: 'auto',
      aspectRatio: 'auto'
    }))
    expect(savedSingleEdit).not.toHaveProperty('resolution')
    expect(savedSingleEdit).not.toHaveProperty('quality')

    await selectSourceFiles(wrapper, [new File(['second'], 'second.jpeg', { type: 'image/jpeg' })])
    expect(wrapper.get('#image-studio-aspect-ratio').exists()).toBe(true)
    await wrapper.get('#image-studio-aspect-ratio').setValue('auto')
    await wrapper.get('#image-studio-prompt').setValue('blend at the source ratio')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(generateImage).toHaveBeenNthCalledWith(2, {
      api_key_id: 10,
      prompt: 'blend at the source ratio',
      model: 'grok-imagine-image-2.0',
      source_images: [
        { mime_type: 'image/png', data: 'Zmlyc3Q=' },
        { mime_type: 'image/jpeg', data: 'c2Vjb25k' }
      ]
    }, expect.any(AbortSignal))

    await wrapper.get('#image-studio-aspect-ratio').setValue('20:9')
    await wrapper.get('#image-studio-prompt').setValue('blend two images')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(generateImage).toHaveBeenNthCalledWith(3, {
      api_key_id: 10,
      prompt: 'blend two images',
      model: 'grok-imagine-image-2.0',
      source_images: [
        { mime_type: 'image/png', data: 'Zmlyc3Q=' },
        { mime_type: 'image/jpeg', data: 'c2Vjb25k' }
      ],
      aspect_ratio: '20:9'
    }, expect.any(AbortSignal))
  })

  it('trims reference images when the selected model has a lower limit', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('#image-studio-key').setValue('8')
    await flushPromises()
    await selectSourceFiles(wrapper, [
      new File(['one'], 'one.png', { type: 'image/png' }),
      new File(['two'], 'two.png', { type: 'image/png' })
    ])
    expect(wrapper.findAll('.source-image-item')).toHaveLength(2)

    await wrapper.get('#image-studio-model').setValue('gemini-2.5-flash-image')
    await flushPromises()

    expect(wrapper.findAll('.source-image-item')).toHaveLength(1)
    expect(wrapper.get('[data-testid="source-image-input"]').attributes('multiple')).toBeUndefined()
    expect(showWarning).toHaveBeenCalledWith('imageStudio.sourceImagesTrimmed')
  })

  it('keeps the composer editable and snapshots concurrent submissions', async () => {
    const first = deferred<ReturnType<typeof generatedImage>>()
    const second = deferred<ReturnType<typeof generatedImage>>()
    generateImage.mockReset()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('#image-studio-size').setValue('1024x1024')
    await wrapper.get('#image-studio-prompt').setValue('first prompt')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('#image-studio-prompt').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('.generate-button').attributes('disabled')).toBeUndefined()

    await wrapper.get('#image-studio-size').setValue('1536x1024')
    await wrapper.get('#image-studio-prompt').setValue('second prompt')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(generateImage).toHaveBeenCalledTimes(2)
    expect(generateImage).toHaveBeenNthCalledWith(1, expect.objectContaining({
      prompt: 'first prompt', size: '1024x1024'
    }), expect.any(AbortSignal))
    expect(generateImage).toHaveBeenNthCalledWith(2, expect.objectContaining({
      prompt: 'second prompt', size: '1536x1024'
    }), expect.any(AbortSignal))
    const firstSignal = generateImage.mock.calls[0][1] as AbortSignal
    const secondSignal = generateImage.mock.calls[1][1] as AbortSignal
    expect(firstSignal).not.toBe(secondSignal)
    expect(wrapper.findAll('[data-testid="generation-placeholder"]')).toHaveLength(2)

    second.resolve(generatedImage('c2Vjb25k'))
    await flushPromises()
    expect(wrapper.findAll('[data-testid="generation-placeholder"]')).toHaveLength(1)
    expect(wrapper.findAll('.gallery-item-meta p').map((item) => item.text())).toEqual(['second prompt'])

    first.resolve(generatedImage('Zmlyc3Q='))
    await flushPromises()
    expect(wrapper.findAll('[data-testid="generation-placeholder"]')).toHaveLength(0)
    expect(saveGallery.mock.calls.map(([item]) => ({ prompt: item.prompt, size: item.size })))
      .toEqual(expect.arrayContaining([
        { prompt: 'first prompt', size: '1024x1024' },
        { prompt: 'second prompt', size: '1536x1024' }
      ]))
  })

  it('keeps provider-specific metadata and format fallbacks with each request', async () => {
    const openAIRequest = deferred<{ data: Array<{ b64_json: string; mime_type?: string }> }>()
    const grokRequest = deferred<{ data: Array<{ b64_json: string; mime_type?: string }> }>()
    generateImage.mockReset()
      .mockReturnValueOnce(openAIRequest.promise)
      .mockReturnValueOnce(grokRequest.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('#image-studio-prompt').setValue('OpenAI prompt')
    await wrapper.findAll('.segmented-control.compact button')[2].trigger('click')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('#image-studio-key').setValue('10')
    await flushPromises()
    await wrapper.get('#image-studio-prompt').setValue('Grok prompt')
    await wrapper.get('#image-studio-aspect-ratio').setValue('20:9')
    await wrapper.get('#image-studio-resolution').setValue('2k')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    grokRequest.resolve({ data: [{ b64_json: 'Z3Jvaw==', mime_type: 'image/jpeg' }] })
    openAIRequest.resolve({ data: [{ b64_json: 'b3BlbmFp' }] })
    await flushPromises()

    const savedByPrompt = Object.fromEntries(saveGallery.mock.calls.map(([item]) => [item.prompt, item]))
    expect(savedByPrompt['OpenAI prompt']).toEqual(expect.objectContaining({
      apiKeyId: 7,
      provider: 'openai',
      model: 'gpt-image-2',
      outputFormat: 'webp',
      imageSrc: 'data:image/webp;base64,b3BlbmFp'
    }))
    expect(savedByPrompt['Grok prompt']).toEqual(expect.objectContaining({
      apiKeyId: 10,
      provider: 'grok',
      model: 'grok-imagine-image-2.0',
      aspectRatio: '20:9',
      resolution: '2k',
      outputFormat: 'jpeg',
      imageSrc: 'data:image/jpeg;base64,Z3Jvaw=='
    }))
  })

  it('isolates failure, cancellation, and retry to their original jobs', async () => {
    const failed = deferred<ReturnType<typeof generatedImage>>()
    const active = deferred<ReturnType<typeof generatedImage>>()
    const retry = deferred<ReturnType<typeof generatedImage>>()
    generateImage.mockReset()
      .mockReturnValueOnce(failed.promise)
      .mockReturnValueOnce(active.promise)
      .mockReturnValueOnce(retry.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('#image-studio-prompt').setValue('failed prompt')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('#image-studio-prompt').setValue('active prompt')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    failed.reject(new Error('request failed'))
    await flushPromises()
    const failedCard = wrapper.findAll('[data-testid="generation-placeholder"]')
      .find((card) => card.text().includes('failed prompt'))!
    const activeCard = wrapper.findAll('[data-testid="generation-placeholder"]')
      .find((card) => card.text().includes('active prompt'))!
    expect(failedCard.text()).toContain('imageStudio.generationFailed')
    expect(activeCard.text()).toContain('imageStudio.generating')

    await activeCard.get('.placeholder-icon-button').trigger('click')
    expect((generateImage.mock.calls[1][1] as AbortSignal).aborted).toBe(true)
    expect(wrapper.findAll('[data-testid="generation-placeholder"]')).toHaveLength(1)
    active.resolve(generatedImage('Y2FuY2VsZWQ='))
    await flushPromises()

    await failedCard.get('.retry-button').trigger('click')
    await flushPromises()
    expect(generateImage).toHaveBeenNthCalledWith(3, expect.objectContaining({ prompt: 'failed prompt' }), expect.any(AbortSignal))
    retry.resolve(generatedImage('cmV0cnk='))
    await flushPromises()
    expect(wrapper.findAll('[data-testid="generation-placeholder"]')).toHaveLength(0)
    expect(saveGallery.mock.calls[saveGallery.mock.calls.length - 1]?.[0].prompt).toBe('failed prompt')
  })

  it('aborts every active generation when the view unmounts', async () => {
    const first = deferred<ReturnType<typeof generatedImage>>()
    const second = deferred<ReturnType<typeof generatedImage>>()
    generateImage.mockReset()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('#image-studio-prompt').setValue('first prompt')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('#image-studio-prompt').setValue('second prompt')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const signals = generateImage.mock.calls.map((call) => call[1] as AbortSignal)

    wrapper.unmount()
    expect(signals).toHaveLength(2)
    expect(signals.every((signal) => signal.aborted)).toBe(true)
    first.resolve(generatedImage('Zmlyc3Q='))
    second.resolve(generatedImage('c2Vjb25k'))
    await flushPromises()
  })

  it('does not notify after unmounting during gallery persistence', async () => {
    const persistence = deferred<void>()
    saveGallery.mockReset().mockReturnValueOnce(persistence.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('#image-studio-prompt').setValue('persisting prompt')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(saveGallery).toHaveBeenCalledTimes(1)
    expect(showSuccess).not.toHaveBeenCalled()

    wrapper.unmount()
    persistence.resolve()
    await flushPromises()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showWarning).not.toHaveBeenCalled()
  })

  it('merges a late local gallery load with newly generated results', async () => {
    const galleryLoad = deferred<any[]>()
    listGallery.mockReset().mockReturnValueOnce(galleryLoad.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('#image-studio-prompt').setValue('new prompt')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.findAll('.gallery-item')).toHaveLength(1)

    galleryLoad.resolve([{
      id: 'stored-image', userId: 42, createdAt: 1, prompt: 'stored prompt', apiKeyId: 7,
      provider: 'openai', model: 'gpt-image-2', size: '1024x1024', outputFormat: 'png',
      imageSrc: 'data:image/png;base64,c3RvcmVk'
    }])
    await flushPromises()

    expect(wrapper.findAll('.gallery-item-meta p').map((item) => item.text()))
      .toEqual(['new prompt', 'stored prompt'])
  })

  it('preserves newly generated results when clearing the gallery fails', async () => {
    const clearing = deferred<void>()
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    listGallery.mockResolvedValueOnce([{
      id: 'stored-image', userId: 42, createdAt: 1, prompt: 'stored prompt', apiKeyId: 7,
      provider: 'openai', model: 'gpt-image-2', size: '1024x1024', outputFormat: 'png',
      imageSrc: 'data:image/png;base64,c3RvcmVk'
    }])
    clearGallery.mockReturnValueOnce(clearing.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('.toolbar-icon-button').trigger('click')
    await wrapper.get('#image-studio-prompt').setValue('new prompt')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    clearing.reject(new Error('clear failed'))
    await flushPromises()

    expect(wrapper.findAll('.gallery-item-meta p').map((item) => item.text()))
      .toEqual(['new prompt', 'stored prompt'])
    confirm.mockRestore()
  })
})
