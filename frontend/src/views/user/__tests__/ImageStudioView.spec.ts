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
    listKeys.mockReset().mockResolvedValue({ items: [activeOpenAIKey, activeGrokKey], pages: 1 })
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
  })

  it('only presents OpenAI image-enabled keys', async () => {
    const wrapper = mountView()
    await flushPromises()

    const options = wrapper.find('#image-studio-key').findAll('option').map((option) => option.text())
    expect(options).toContain('OpenAI image key')
    expect(options).not.toContain('Grok image key')
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
