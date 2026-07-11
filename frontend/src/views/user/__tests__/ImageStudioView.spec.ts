import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ImageStudioView from '../ImageStudioView.vue'

const listKeys = vi.hoisted(() => vi.fn())
const generateImage = vi.hoisted(() => vi.fn())
const listImageStudioGallery = vi.hoisted(() => vi.fn())
const saveImageStudioGalleryItem = vi.hoisted(() => vi.fn())
const showSuccess = vi.hoisted(() => vi.fn())
const showWarning = vi.hoisted(() => vi.fn())

vi.mock('@/api/keys', () => ({ keysAPI: { list: listKeys } }))
vi.mock('@/api/imagePlayground', () => ({ generateImage }))
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
    useI18n: () => ({ t: (key: string) => key })
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
