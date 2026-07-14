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
  cleanupAttachments: vi.fn(),
  clearAttachments: vi.fn(),
  deleteAttachment: vi.fn(),
  deleteConversationAttachments: vi.fn(),
  listDrafts: vi.fn(),
  listConversationAttachments: vi.fn(),
  saveDraft: vi.fn(),
  reassignDrafts: vi.fn(),
  bindToMessage: vi.fn(),
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

vi.mock('@/utils/chatAttachmentStore', () => ({
  bindChatAttachmentsToMessage: mocks.bindToMessage,
  chatAttachmentDraftKey: (conversationId: number) => `chat-${conversationId}`,
  cleanupUserChatAttachments: mocks.cleanupAttachments,
  clearUserChatAttachments: mocks.clearAttachments,
  deleteChatAttachment: mocks.deleteAttachment,
  deleteChatAttachmentsForConversation: mocks.deleteConversationAttachments,
  listChatAttachmentDrafts: mocks.listDrafts,
  listChatConversationAttachments: mocks.listConversationAttachments,
  reassignChatAttachmentDrafts: mocks.reassignDrafts,
  saveChatAttachmentDraft: mocks.saveDraft,
}))

vi.mock('@/utils/chatMarkdown', () => ({ renderChatMarkdown: (value: string) => value }))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

function conversation(id: number, title = `Chat ${id}`) {
  return {
    id,
    user_id: 42,
    title,
    api_key_id: 7,
    model: 'gpt-5',
    system_prompt: '',
    reasoning_effort: '',
    message_count: 0,
    created_at: id,
    updated_at: id,
  }
}

function message(id: number, role: 'user' | 'assistant', content: string) {
  return {
    id,
    conversation_id: 99,
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

function page(items: ReturnType<typeof conversation>[], total: number, currentPage = 1) {
  return {
    items,
    total,
    page: currentPage,
    page_size: 12,
    pages: Math.max(1, Math.ceil(total / 12)),
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

function historyTitles(wrapper: ReturnType<typeof mountView>): string[] {
  return wrapper.findAll('.history-title').map((item) => item.text())
}

describe('ChatView recent history', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    mocks.listKeys.mockResolvedValue({ items: [{ id: 7, name: 'Key', status: 'active' }] })
    mocks.listModels.mockResolvedValue({ models: ['gpt-5'] })
    mocks.listConversations.mockResolvedValue(page(
      Array.from({ length: 12 }, (_, index) => conversation(index + 1)),
      25,
    ))
    mocks.createConversation.mockResolvedValue(conversation(99, 'New conversation'))
    mocks.updateConversation.mockImplementation(async (id: number, payload: { title?: string }) => conversation(id, payload.title || `Chat ${id}`))
    mocks.deleteConversation.mockResolvedValue({ message: 'deleted' })
    mocks.appendMessage.mockResolvedValue(message(100, 'user', 'New conversation'))
    mocks.streamConversationMessage.mockResolvedValue(message(101, 'assistant', 'Done'))
    mocks.cleanupAttachments.mockResolvedValue(undefined)
    mocks.clearAttachments.mockResolvedValue(undefined)
    mocks.deleteAttachment.mockResolvedValue(undefined)
    mocks.deleteConversationAttachments.mockResolvedValue(undefined)
    mocks.listDrafts.mockResolvedValue([])
    mocks.listConversationAttachments.mockResolvedValue([])
    mocks.saveDraft.mockResolvedValue(undefined)
    mocks.reassignDrafts.mockResolvedValue(undefined)
    mocks.bindToMessage.mockResolvedValue(undefined)
  })

  it('loads only the first 12 recent conversations', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(mocks.listConversations).toHaveBeenCalledWith(1, 12)
    expect(historyTitles(wrapper)).toHaveLength(12)
    expect(wrapper.get('.history-pagination-button').text()).toContain('chat.loadOlderChats')
  })

  it('shows the last-updated date and time for every conversation', async () => {
    const wrapper = mountView()
    await flushPromises()

    const timestamps = wrapper.findAll('time.history-datetime')
    expect(timestamps).toHaveLength(12)
    expect(timestamps.every((timestamp) => timestamp.text().trim().length > 0)).toBe(true)
    expect(timestamps[0].attributes('datetime')).toBe('1970-01-01T00:00:01.000Z')
  })

  it('loads older conversations without duplicates and can collapse to recent chats', async () => {
    mocks.listConversations
      .mockResolvedValueOnce(page(Array.from({ length: 12 }, (_, index) => conversation(index + 1)), 25))
      .mockResolvedValueOnce(page([conversation(12), ...Array.from({ length: 11 }, (_, index) => conversation(index + 13))], 25, 2))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('.history-pagination-button').trigger('click')
    await flushPromises()

    expect(mocks.listConversations).toHaveBeenLastCalledWith(2, 12)
    expect(historyTitles(wrapper)).toHaveLength(23)
    expect(new Set(historyTitles(wrapper)).size).toBe(23)

    const showFewer = wrapper.findAll('.history-pagination-button')
      .find((button) => button.text().includes('chat.showFewerChats'))
    expect(showFewer).toBeDefined()
    await showFewer!.trigger('click')
    expect(historyTitles(wrapper)).toHaveLength(12)
  })

  it('keeps existing chats when loading older history fails', async () => {
    mocks.listConversations
      .mockResolvedValueOnce(page(Array.from({ length: 12 }, (_, index) => conversation(index + 1)), 25))
      .mockRejectedValueOnce(new Error('history offline'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('.history-pagination-button').trigger('click')
    await flushPromises()

    expect(historyTitles(wrapper)).toHaveLength(12)
    expect(mocks.showError).toHaveBeenCalledWith('history offline')
  })

  it('ignores a stale older-history response after deletion refills the window', async () => {
    let resolveOlder!: (value: ReturnType<typeof page>) => void
    mocks.listConversations
      .mockResolvedValueOnce(page(Array.from({ length: 12 }, (_, index) => conversation(index + 1)), 25))
      .mockImplementationOnce(() => new Promise((resolve) => { resolveOlder = resolve }))
      .mockResolvedValueOnce(page(Array.from({ length: 12 }, (_, index) => conversation(index + 2)), 24))
    const wrapper = mountView()
    await flushPromises()

    const loadOlder = wrapper.get('.history-pagination-button')
    await loadOlder.trigger('click')
    expect(loadOlder.attributes('disabled')).toBeDefined()

    const first = wrapper.findAll('.history-item')[0]
    const remove = first.findAll('button').find((button) => button.attributes('title') === 'chat.delete')
    await remove!.trigger('click')
    await wrapper.get('.confirm-dialog-stub').trigger('click')
    await flushPromises()

    resolveOlder(page(Array.from({ length: 12 }, (_, index) => conversation(index + 13)), 25, 2))
    await flushPromises()

    expect(mocks.listConversations).toHaveBeenLastCalledWith(1, 12)
    expect(historyTitles(wrapper)).toEqual(Array.from({ length: 12 }, (_, index) => `Chat ${index + 2}`))
  })

  it('places a new chat first without growing the default window beyond 12', async () => {
    mocks.listConversations.mockResolvedValueOnce(page(
      Array.from({ length: 12 }, (_, index) => conversation(index + 1)),
      12,
    ))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('textarea.composer-input').setValue('New conversation')
    const send = wrapper.get('button[type="submit"]')
    expect(send.attributes('disabled')).toBeUndefined()
    await wrapper.get('textarea.composer-input').trigger('keydown', { key: 'Enter' })
    await vi.waitFor(() => expect(mocks.createConversation).toHaveBeenCalledTimes(1))
    await flushPromises()

    expect(mocks.listConversations).toHaveBeenCalledTimes(1)
    expect(historyTitles(wrapper)).toHaveLength(12)
    expect(historyTitles(wrapper)[0]).toBe('New conversation')
    expect(historyTitles(wrapper)).not.toContain('Chat 12')
  })

  it('moves a renamed conversation to the top', async () => {
    const wrapper = mountView()
    await flushPromises()

    const target = wrapper.findAll('.history-item').find((item) => item.text().includes('Chat 5'))
    expect(target).toBeDefined()
    const rename = target!.findAll('button').find((button) => button.attributes('title') === 'chat.rename')
    expect(rename).toBeDefined()
    await rename!.trigger('click')
    await target!.get('input').setValue('Renamed chat')
    await target!.get('input').trigger('keydown', { key: 'Enter' })
    await flushPromises()

    expect(historyTitles(wrapper)[0]).toBe('Renamed chat')
  })

  it('refills the visible window after deleting a conversation', async () => {
    mocks.listConversations
      .mockResolvedValueOnce(page(Array.from({ length: 12 }, (_, index) => conversation(index + 1)), 13))
      .mockResolvedValueOnce(page(Array.from({ length: 12 }, (_, index) => conversation(index + 2)), 12))
    const wrapper = mountView()
    await flushPromises()

    const first = wrapper.findAll('.history-item')[0]
    const remove = first.findAll('button').find((button) => button.attributes('title') === 'chat.delete')
    expect(remove).toBeDefined()
    await remove!.trigger('click')
    await wrapper.get('.confirm-dialog-stub').trigger('click')
    await flushPromises()

    expect(mocks.listConversations).toHaveBeenLastCalledWith(1, 12)
    expect(historyTitles(wrapper)).toHaveLength(12)
    expect(historyTitles(wrapper)).toContain('Chat 13')
  })
})
