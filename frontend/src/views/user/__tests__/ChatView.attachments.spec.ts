import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ChatView from '../ChatView.vue'

const mocks = vi.hoisted(() => ({
  listModels: vi.fn(),
  listConversations: vi.fn(),
  createConversation: vi.fn(),
  getConversation: vi.fn(),
  updateConversation: vi.fn(),
  deleteConversation: vi.fn(),
  appendMessage: vi.fn(),
  deleteMessage: vi.fn(),
  streamConversationMessage: vi.fn(),
  listKeys: vi.fn(),
  saveDraft: vi.fn(),
  reassignDrafts: vi.fn(),
  bindToMessage: vi.fn(),
  cleanup: vi.fn(),
  clearUser: vi.fn(),
  deleteAttachment: vi.fn(),
  deleteConversationAttachments: vi.fn(),
  listDrafts: vi.fn(),
  listConversationAttachments: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/chat', () => ({
  default: {
    listModels: mocks.listModels,
    listConversations: mocks.listConversations,
    createConversation: mocks.createConversation,
    getConversation: mocks.getConversation,
    updateConversation: mocks.updateConversation,
    deleteConversation: mocks.deleteConversation,
    appendMessage: mocks.appendMessage,
    deleteMessage: mocks.deleteMessage,
    streamConversationMessage: mocks.streamConversationMessage,
    savedMessageFromChatStreamError: () => null,
  }
}))

vi.mock('@/api/keys', () => ({
  default: { list: mocks.listKeys },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: mocks.showSuccess,
    showWarning: mocks.showWarning,
    showError: mocks.showError,
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 42 } })
}))

vi.mock('@/utils/chatAttachmentStore', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/utils/chatAttachmentStore')>()
  return {
    ...actual,
    saveChatAttachmentDraft: mocks.saveDraft,
    reassignChatAttachmentDrafts: mocks.reassignDrafts,
    bindChatAttachmentsToMessage: mocks.bindToMessage,
    cleanupUserChatAttachments: mocks.cleanup,
    clearUserChatAttachments: mocks.clearUser,
    deleteChatAttachment: mocks.deleteAttachment,
    deleteChatAttachmentsForConversation: mocks.deleteConversationAttachments,
    listChatAttachmentDrafts: mocks.listDrafts,
    listChatConversationAttachments: mocks.listConversationAttachments,
  }
})

vi.mock('@/utils/chatMarkdown', () => ({ renderChatMarkdown: (value: string) => value }))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

function message(id: number, role: 'user' | 'assistant', content: string) {
  return {
    id,
    conversation_id: 5,
    user_id: 42,
    role,
    content,
    status: 'complete',
    error_message: '',
    metadata: {},
    created_at: 1,
    updated_at: 1,
  }
}

function mountView() {
  return mount(ChatView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        ConfirmDialog: {
          props: ['show'],
          emits: ['confirm'],
          template: '<button v-if="show" class="confirm-dialog-stub" @click="$emit(\'confirm\')">confirm</button>',
        },
        Icon: { template: '<i />' },
        RouterLink: { template: '<a><slot /></a>' },
      }
    }
  })
}

describe('ChatView local attachments', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.listKeys.mockResolvedValue({ items: [{ id: 7, name: 'Key', status: 'active' }] })
    mocks.listModels.mockResolvedValue({ models: ['gpt-5'] })
    mocks.listConversations.mockResolvedValue({ items: [], pages: 1 })
    mocks.createConversation.mockResolvedValue({
      id: 5,
      user_id: 42,
      title: 'Attachment',
      api_key_id: 7,
      model: 'gpt-5',
      system_prompt: '',
      reasoning_effort: '',
      message_count: 0,
      created_at: 1,
      updated_at: 1,
    })
    mocks.appendMessage.mockResolvedValue(message(11, 'user', 'chat.attachmentOnlyMessage'))
    mocks.streamConversationMessage.mockResolvedValue(message(12, 'assistant', 'done'))
    mocks.saveDraft.mockResolvedValue(undefined)
    mocks.reassignDrafts.mockResolvedValue(undefined)
    mocks.bindToMessage.mockResolvedValue(undefined)
    mocks.cleanup.mockResolvedValue(undefined)
    mocks.clearUser.mockResolvedValue(undefined)
    mocks.deleteAttachment.mockResolvedValue(undefined)
    mocks.deleteConversationAttachments.mockResolvedValue(undefined)
    mocks.listDrafts.mockResolvedValue([])
    mocks.listConversationAttachments.mockResolvedValue([])
    let objectUrl = 0
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      value: vi.fn(() => `blob:chat-${++objectUrl}`),
    })
    Object.defineProperty(URL, 'revokeObjectURL', {
      configurable: true,
      value: vi.fn(),
    })
  })

  it('stores image blobs locally, binds them to the saved message, and only base64-encodes the stream payload', async () => {
    const wrapper = mountView()
    await flushPromises()

    const input = wrapper.get('input[accept="image/*"]')
    const file = new File(['image-bytes'], 'sample.png', { type: 'image/png' })
    Object.defineProperty(input.element, 'files', { configurable: true, value: [file] })
    await input.trigger('change')
    await flushPromises()

    const stored = mocks.saveDraft.mock.calls[0][0]
    expect(stored.userId).toBe(42)
    expect(stored.blob).toBe(file)
    expect(stored).not.toHaveProperty('data_url')

    await wrapper.get('form.composer').trigger('submit')
    await flushPromises()

    const attachmentId = stored.id
    expect(mocks.reassignDrafts).toHaveBeenCalledWith(42, [attachmentId], 5)
    expect(mocks.bindToMessage).toHaveBeenCalledWith(42, [attachmentId], 5, 11, true)
    await vi.waitFor(() => expect(mocks.streamConversationMessage).toHaveBeenCalledTimes(1))
    const streamOptions = mocks.streamConversationMessage.mock.calls[0][0]
    expect(streamOptions.attachments[0].data_url).toMatch(/^data:image\/png;base64,/)
    wrapper.unmount()
    expect(URL.revokeObjectURL).toHaveBeenCalled()
  })

  it('hides the waiting animation immediately when streaming is stopped', async () => {
    mocks.appendMessage.mockResolvedValue(message(11, 'user', 'hello'))
    mocks.streamConversationMessage.mockReturnValue(new Promise(() => {}))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('textarea.composer-input').setValue('hello')
    await wrapper.get('form.composer').trigger('submit')
    await vi.waitFor(() => expect(mocks.streamConversationMessage).toHaveBeenCalledTimes(1))
    expect(wrapper.find('.typing-indicator').exists()).toBe(true)

    const streamOptions = mocks.streamConversationMessage.mock.calls[0][0]
    await wrapper.get('.composer-button.stop').trigger('click')

    expect(streamOptions.signal.aborted).toBe(true)
    expect(wrapper.find('.typing-indicator').exists()).toBe(false)
    expect(wrapper.text()).toContain('chat.stopped')
    wrapper.unmount()
  })

  it('hydrates message attachments from IndexedDB when a conversation is opened', async () => {
    const conversation = {
      id: 5,
      user_id: 42,
      title: 'Saved chat',
      api_key_id: 7,
      model: 'gpt-5',
      system_prompt: '',
      reasoning_effort: '',
      message_count: 1,
      messages: [message(20, 'user', 'look')],
      created_at: 1,
      updated_at: 1,
    }
    mocks.listConversations.mockResolvedValue({ items: [conversation], pages: 1 })
    mocks.getConversation.mockResolvedValue(conversation)
    mocks.listConversationAttachments.mockResolvedValue([{
      id: 'stored-image',
      userId: 42,
      conversationId: 5,
      messageId: 20,
      draftKey: '',
      type: 'image',
      name: 'saved.png',
      mimeType: 'image/png',
      size: 3,
      blob: new Blob(['abc'], { type: 'image/png' }),
      attachmentOnly: true,
      createdAt: 1,
      lastAccessedAt: 1,
    }])

    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('.history-item').trigger('click')
    await flushPromises()

    expect(mocks.listConversationAttachments).toHaveBeenCalledWith(42, 5)
    expect(wrapper.get('.message-attachment-image').attributes('src')).toMatch(/^blob:chat-/)
    expect(wrapper.text()).toContain('saved.png')
  })

  it('clears the current user local attachments from chat settings', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('.history-footer .history-command').trigger('click')
    const settingsButtons = wrapper.findAll('.settings-field .btn')
    await settingsButtons[settingsButtons.length - 1].trigger('click')
    await wrapper.get('.confirm-dialog-stub').trigger('click')
    await flushPromises()

    expect(mocks.clearUser).toHaveBeenCalledWith(42)
    expect(mocks.showSuccess).toHaveBeenCalledWith('chat.localAttachmentsCleared')
  })

  it('does not let delayed startup cleanup replace a conversation opened by the user', async () => {
    let finishCleanup!: () => void
    mocks.cleanup.mockReturnValue(new Promise<void>((resolve) => { finishCleanup = resolve }))
    const conversation = {
      id: 5,
      user_id: 42,
      title: 'Opened quickly',
      api_key_id: 7,
      model: 'gpt-5',
      system_prompt: '',
      reasoning_effort: '',
      message_count: 1,
      messages: [message(20, 'user', 'keep this message')],
      created_at: 1,
      updated_at: 1,
    }
    mocks.listConversations.mockResolvedValue({ items: [conversation], pages: 1 })
    mocks.getConversation.mockResolvedValue(conversation)

    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('.history-item').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('keep this message')

    finishCleanup()
    await flushPromises()
    expect(wrapper.text()).toContain('keep this message')
  })

  it('reassigns new-chat drafts before append so a failed first message remains scoped to the created conversation', async () => {
    mocks.appendMessage.mockRejectedValue(new Error('append failed'))
    const wrapper = mountView()
    await flushPromises()

    const input = wrapper.get('input[accept="image/*"]')
    const file = new File(['image-bytes'], 'retry.png', { type: 'image/png' })
    Object.defineProperty(input.element, 'files', { configurable: true, value: [file] })
    await input.trigger('change')
    await flushPromises()
    const attachmentId = mocks.saveDraft.mock.calls[0][0].id

    await wrapper.get('form.composer').trigger('submit')
    await flushPromises()

    expect(mocks.reassignDrafts).toHaveBeenCalledWith(42, [attachmentId], 5)
    expect(mocks.bindToMessage).not.toHaveBeenCalled()
    expect(wrapper.get('.attachment-chip').text()).toContain('retry.png')
  })

  it('revokes existing Blob previews when attachment restoration fails', async () => {
    const draftBlob = new Blob(['draft'], { type: 'image/png' })
    mocks.listDrafts.mockResolvedValueOnce([{
      id: 'draft-image',
      userId: 42,
      conversationId: 0,
      messageId: 0,
      draftKey: 'new',
      type: 'image',
      name: 'draft.png',
      mimeType: 'image/png',
      size: draftBlob.size,
      blob: draftBlob,
      createdAt: 1,
      lastAccessedAt: 1,
    }]).mockResolvedValue([])
    const conversation = {
      id: 5,
      user_id: 42,
      title: 'Broken storage',
      api_key_id: 7,
      model: 'gpt-5',
      system_prompt: '',
      reasoning_effort: '',
      message_count: 1,
      messages: [message(20, 'user', 'message still loads')],
      created_at: 1,
      updated_at: 1,
    }
    mocks.listConversations.mockResolvedValue({ items: [conversation], pages: 1 })
    mocks.getConversation.mockResolvedValue(conversation)
    mocks.listConversationAttachments.mockRejectedValue(new Error('indexeddb failed'))

    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('.attachment-thumb').attributes('src')).toMatch(/^blob:chat-/)

    await wrapper.get('.history-item').trigger('click')
    await flushPromises()

    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:chat-1')
    expect(wrapper.text()).toContain('message still loads')
  })
})
