export type StoredChatAttachmentType = 'image' | 'file'

export interface StoredChatAttachment {
  id: string
  userId: number
  conversationId: number
  messageId: number
  draftKey: string
  type: StoredChatAttachmentType
  name: string
  mimeType: string
  size: number
  blob?: Blob
  text?: string
  attachmentOnly?: boolean
  createdAt: number
  lastAccessedAt: number
}

export const chatAttachmentRetentionMs = 30 * 24 * 60 * 60 * 1000
export const chatAttachmentMaxUserBytes = 250 * 1024 * 1024

const databaseName = 'sub2api-chat'
const databaseVersion = 1
const storeName = 'attachments'
const userIndex = 'by_user'
const conversationIndex = 'by_user_conversation'
const messageIndex = 'by_user_message'
const draftIndex = 'by_user_draft'

export class ChatAttachmentStorageQuotaError extends Error {
  constructor() {
    super('Local chat attachment storage is full')
    this.name = 'ChatAttachmentStorageQuotaError'
  }
}

function hasIndexedDB(): boolean {
  return typeof window !== 'undefined' && 'indexedDB' in window
}

function request<T>(value: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    value.onsuccess = () => resolve(value.result)
    value.onerror = () => reject(value.error)
  })
}

function transactionDone(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.oncomplete = () => resolve()
    transaction.onerror = () => reject(transaction.error)
    transaction.onabort = () => reject(transaction.error || new Error('IndexedDB transaction aborted'))
  })
}

async function openDatabase(): Promise<IDBDatabase | null> {
  if (!hasIndexedDB()) return null
  return new Promise((resolve, reject) => {
    const open = window.indexedDB.open(databaseName, databaseVersion)
    open.onupgradeneeded = () => {
      const store = open.result.objectStoreNames.contains(storeName)
        ? open.transaction!.objectStore(storeName)
        : open.result.createObjectStore(storeName, { keyPath: 'id' })
      if (!store.indexNames.contains(userIndex)) store.createIndex(userIndex, 'userId')
      if (!store.indexNames.contains(conversationIndex)) store.createIndex(conversationIndex, ['userId', 'conversationId'])
      if (!store.indexNames.contains(messageIndex)) store.createIndex(messageIndex, ['userId', 'messageId'])
      if (!store.indexNames.contains(draftIndex)) store.createIndex(draftIndex, ['userId', 'draftKey'])
    }
    open.onsuccess = () => resolve(open.result)
    open.onerror = () => reject(open.error)
  })
}

async function userRecords(db: IDBDatabase, userId: number): Promise<StoredChatAttachment[]> {
  const transaction = db.transaction(storeName, 'readonly')
  return request(transaction.objectStore(storeName).index(userIndex).getAll(IDBKeyRange.only(userId)))
}

async function deleteRecords(db: IDBDatabase, ids: string[]): Promise<void> {
  if (ids.length === 0) return
  const transaction = db.transaction(storeName, 'readwrite')
  const done = transactionDone(transaction)
  const store = transaction.objectStore(storeName)
  for (const id of ids) store.delete(id)
  await done
}

async function ensureBrowserCapacity(incomingBytes: number): Promise<void> {
  if (typeof navigator === 'undefined' || !navigator.storage?.estimate) return
  const estimate = await navigator.storage.estimate()
  if (!estimate.quota || estimate.usage === undefined) return
  if (estimate.usage + incomingBytes > estimate.quota * 0.95) {
    throw new ChatAttachmentStorageQuotaError()
  }
}

async function enforceUserBudget(
  db: IDBDatabase,
  userId: number,
  incomingBytes: number,
  replacingId: string,
  now: number,
): Promise<number> {
  if (incomingBytes > chatAttachmentMaxUserBytes) throw new ChatAttachmentStorageQuotaError()
  const records = await userRecords(db, userId)
  const replacedBytes = records.find((item) => item.id === replacingId)?.size || 0
  const eviction = planChatAttachmentEvictions(records, incomingBytes, replacingId, now)
  await deleteRecords(db, eviction.deleteIds)
  if (eviction.retainedBytes + incomingBytes > chatAttachmentMaxUserBytes) {
    throw new ChatAttachmentStorageQuotaError()
  }
  return Math.max(0, incomingBytes - replacedBytes)
}

export function planChatAttachmentEvictions(
  records: StoredChatAttachment[],
  incomingBytes: number,
  replacingId: string,
  now: number,
): { deleteIds: string[]; retainedBytes: number } {
  const expiredBefore = now - chatAttachmentRetentionMs
  const expired = records.filter((item) => item.createdAt < expiredBefore)
  const retained = records.filter((item) => item.createdAt >= expiredBefore && item.id !== replacingId)
  const deleteIds = expired.map((item) => item.id)
  let totalBytes = retained.reduce((total, item) => total + item.size, 0)
  for (const item of [...retained].sort((a, b) => a.lastAccessedAt - b.lastAccessedAt)) {
    if (totalBytes + incomingBytes <= chatAttachmentMaxUserBytes) break
    deleteIds.push(item.id)
    totalBytes -= item.size
  }
  return { deleteIds, retainedBytes: totalBytes }
}

export function chatAttachmentDraftKey(conversationId: number | null | undefined): string {
  return conversationId && conversationId > 0 ? `conversation:${conversationId}` : 'new'
}

export async function saveChatAttachmentDraft(record: StoredChatAttachment): Promise<void> {
  if (record.userId <= 0) return
  const db = await openDatabase()
  if (!db) return
  try {
    const additionalBytes = await enforceUserBudget(db, record.userId, record.size, record.id, Date.now())
    await ensureBrowserCapacity(additionalBytes)
    const transaction = db.transaction(storeName, 'readwrite')
    const done = transactionDone(transaction)
    transaction.objectStore(storeName).put(record)
    await done
  } finally {
    db.close()
  }
}

export async function bindChatAttachmentsToMessage(
  userId: number,
  ids: string[],
  conversationId: number,
  messageId: number,
  attachmentOnly = false,
): Promise<void> {
  if (userId <= 0 || ids.length === 0) return
  const db = await openDatabase()
  if (!db) return
  try {
    const records = await userRecords(db, userId)
    const wanted = new Set(ids)
    const updates = records.filter((item) => wanted.has(item.id))
    if (updates.length === 0) return
    const transaction = db.transaction(storeName, 'readwrite')
    const done = transactionDone(transaction)
    const store = transaction.objectStore(storeName)
    const now = Date.now()
    for (const item of updates) {
      store.put({
        ...item,
        conversationId,
        messageId,
        draftKey: '',
        attachmentOnly,
        lastAccessedAt: now,
      })
    }
    await done
  } finally {
    db.close()
  }
}

export async function reassignChatAttachmentDrafts(
  userId: number,
  ids: string[],
  conversationId: number,
): Promise<void> {
  if (userId <= 0 || conversationId <= 0 || ids.length === 0) return
  const db = await openDatabase()
  if (!db) return
  try {
    const records = await userRecords(db, userId)
    const wanted = new Set(ids)
    const updates = records.filter((item) => wanted.has(item.id))
    if (updates.length === 0) return
    const transaction = db.transaction(storeName, 'readwrite')
    const done = transactionDone(transaction)
    const store = transaction.objectStore(storeName)
    const now = Date.now()
    for (const item of updates) {
      store.put({
        ...item,
        conversationId,
        messageId: 0,
        draftKey: chatAttachmentDraftKey(conversationId),
        attachmentOnly: false,
        lastAccessedAt: now,
      })
    }
    await done
  } finally {
    db.close()
  }
}

export async function listChatAttachmentDrafts(userId: number, draftKey: string): Promise<StoredChatAttachment[]> {
  if (userId <= 0) return []
  const db = await openDatabase()
  if (!db) return []
  try {
    const transaction = db.transaction(storeName, 'readonly')
    const records = await request(transaction.objectStore(storeName).index(draftIndex).getAll([userId, draftKey]))
    return records.sort((a, b) => a.createdAt - b.createdAt)
  } finally {
    db.close()
  }
}

export async function listChatConversationAttachments(userId: number, conversationId: number): Promise<StoredChatAttachment[]> {
  if (userId <= 0 || conversationId <= 0) return []
  const db = await openDatabase()
  if (!db) return []
  try {
    const transaction = db.transaction(storeName, 'readonly')
    const records = await request(transaction.objectStore(storeName).index(conversationIndex).getAll([userId, conversationId]))
    return records.filter((item) => item.messageId > 0).sort((a, b) => a.createdAt - b.createdAt)
  } finally {
    db.close()
  }
}

export async function deleteChatAttachment(userId: number, id: string): Promise<void> {
  if (userId <= 0) return
  const db = await openDatabase()
  if (!db) return
  try {
    const records = await userRecords(db, userId)
    if (records.some((item) => item.id === id)) await deleteRecords(db, [id])
  } finally {
    db.close()
  }
}

export async function deleteChatAttachmentsForMessage(userId: number, messageId: number): Promise<void> {
  if (userId <= 0 || messageId <= 0) return
  const db = await openDatabase()
  if (!db) return
  try {
    const transaction = db.transaction(storeName, 'readonly')
    const records = await request(transaction.objectStore(storeName).index(messageIndex).getAll([userId, messageId]))
    await deleteRecords(db, records.map((item) => item.id))
  } finally {
    db.close()
  }
}

export async function deleteChatAttachmentsForConversation(userId: number, conversationId: number): Promise<void> {
  if (userId <= 0 || conversationId <= 0) return
  const db = await openDatabase()
  if (!db) return
  try {
    const transaction = db.transaction(storeName, 'readonly')
    const records = await request(transaction.objectStore(storeName).index(conversationIndex).getAll([userId, conversationId]))
    await deleteRecords(db, records.map((item) => item.id))
  } finally {
    db.close()
  }
}

export async function clearUserChatAttachments(userId: number): Promise<void> {
  if (userId <= 0) return
  const db = await openDatabase()
  if (!db) return
  try {
    const records = await userRecords(db, userId)
    await deleteRecords(db, records.map((item) => item.id))
  } finally {
    db.close()
  }
}

export async function cleanupUserChatAttachments(userId: number, now = Date.now()): Promise<void> {
  if (userId <= 0) return
  const db = await openDatabase()
  if (!db) return
  try {
    await enforceUserBudget(db, userId, 0, '', now)
  } finally {
    db.close()
  }
}
