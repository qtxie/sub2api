import { IDBFactory, IDBKeyRange } from 'fake-indexeddb'
import { Blob as NodeBlob } from 'node:buffer'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ChatAttachmentStorageQuotaError,
  bindChatAttachmentsToMessage,
  chatAttachmentDraftKey,
  chatAttachmentMaxUserBytes,
  chatAttachmentRetentionMs,
  cleanupUserChatAttachments,
  clearUserChatAttachments,
  deleteChatAttachment,
  deleteChatAttachmentsForMessage,
  listChatAttachmentDrafts,
  listChatConversationAttachments,
  planChatAttachmentEvictions,
  reassignChatAttachmentDrafts,
  saveChatAttachmentDraft,
  type StoredChatAttachment,
} from '../chatAttachmentStore'

function record(id: string, size: number, createdAt: number, lastAccessedAt = createdAt): StoredChatAttachment {
  return {
    id,
    userId: 42,
    conversationId: 1,
    messageId: 1,
    draftKey: '',
    type: 'image',
    name: `${id}.png`,
    mimeType: 'image/png',
    size,
    createdAt,
    lastAccessedAt,
  }
}

describe('chat attachment storage policy', () => {
  beforeEach(() => {
    Object.defineProperty(window, 'indexedDB', {
      configurable: true,
      value: new IDBFactory(),
    })
    vi.stubGlobal('IDBKeyRange', IDBKeyRange)
    vi.stubGlobal('Blob', NodeBlob)
    Object.defineProperty(navigator, 'storage', {
      configurable: true,
      value: undefined,
    })
  })

  it('uses stable draft namespaces for new and existing conversations', () => {
    expect(chatAttachmentDraftKey(null)).toBe('new')
    expect(chatAttachmentDraftKey(0)).toBe('new')
    expect(chatAttachmentDraftKey(17)).toBe('conversation:17')
  })

  it('evicts expired and least-recently-used records before exceeding the user budget', () => {
    const now = Date.now()
    const old = record('expired', 10, now - chatAttachmentRetentionMs - 1)
    const leastRecent = record('least-recent', 100 * 1024 * 1024, now - 1000, now - 1000)
    const recent = record('recent', 100 * 1024 * 1024, now - 500, now - 100)

    const result = planChatAttachmentEvictions(
      [old, leastRecent, recent],
      100 * 1024 * 1024,
      '',
      now,
    )

    expect(result.deleteIds).toEqual(['expired', 'least-recent'])
    expect(result.retainedBytes + 100 * 1024 * 1024).toBeLessThanOrEqual(chatAttachmentMaxUserBytes)
  })

  it('does not count a replaced record twice', () => {
    const now = Date.now()
    const existing = record('same', chatAttachmentMaxUserBytes, now)
    const result = planChatAttachmentEvictions([existing], chatAttachmentMaxUserBytes, 'same', now)

    expect(result.deleteIds).toEqual([])
    expect(result.retainedBytes).toBe(0)
  })

  it('persists Blob drafts and isolates reads and deletes by user', async () => {
    const now = Date.now()
    const userImage = { ...record('user-image', 5, now), messageId: 0, draftKey: 'new', blob: new Blob(['image'], { type: 'image/png' }) }
    const otherImage = { ...record('other-image', 5, now), userId: 7, messageId: 0, draftKey: 'new', blob: new Blob(['other'], { type: 'image/png' }) }
    await saveChatAttachmentDraft(userImage)
    await saveChatAttachmentDraft(otherImage)

    const drafts = await listChatAttachmentDrafts(42, 'new')
    expect(drafts).toHaveLength(1)
    expect(drafts[0].id).toBe('user-image')
    expect(drafts[0].blob).toBeInstanceOf(Blob)
    expect(drafts[0].blob?.size).toBe(5)

    await deleteChatAttachment(42, 'other-image')
    expect(await listChatAttachmentDrafts(7, 'new')).toHaveLength(1)
    await clearUserChatAttachments(42)
    expect(await listChatAttachmentDrafts(42, 'new')).toEqual([])
  })

  it('moves drafts to a conversation, binds them to a message, and deletes through the message index', async () => {
    const now = Date.now()
    await saveChatAttachmentDraft({
      ...record('bound-image', 3, now),
      conversationId: 0,
      messageId: 0,
      draftKey: 'new',
      blob: new Blob(['abc'], { type: 'image/png' }),
    })

    await reassignChatAttachmentDrafts(42, ['bound-image'], 9)
    expect(await listChatAttachmentDrafts(42, 'new')).toEqual([])
    expect(await listChatAttachmentDrafts(42, 'conversation:9')).toHaveLength(1)

    await bindChatAttachmentsToMessage(42, ['bound-image'], 9, 12, true)
    const attachments = await listChatConversationAttachments(42, 9)
    expect(attachments).toHaveLength(1)
    expect(attachments[0]).toMatchObject({ messageId: 12, attachmentOnly: true, draftKey: '' })

    await deleteChatAttachmentsForMessage(42, 12)
    expect(await listChatConversationAttachments(42, 9)).toEqual([])
  })

  it('removes expired records through a real cleanup transaction', async () => {
    const now = Date.now()
    await saveChatAttachmentDraft({
      ...record('expired-record', 1, now - chatAttachmentRetentionMs - 1),
      messageId: 0,
      draftKey: 'new',
      blob: new Blob(['x']),
    })

    await cleanupUserChatAttachments(42, now)
    expect(await listChatAttachmentDrafts(42, 'new')).toEqual([])
  })

  it('rejects writes that exceed the browser quota without persisting a partial record', async () => {
    Object.defineProperty(navigator, 'storage', {
      configurable: true,
      value: { estimate: vi.fn().mockResolvedValue({ quota: 100, usage: 96 }) },
    })
    const now = Date.now()
    const quotaRecord = {
      ...record('quota-record', 10, now),
      messageId: 0,
      draftKey: 'new',
      blob: new Blob(['0123456789']),
    }

    await expect(saveChatAttachmentDraft(quotaRecord)).rejects.toBeInstanceOf(ChatAttachmentStorageQuotaError)
    expect(await listChatAttachmentDrafts(42, 'new')).toEqual([])
  })
})
