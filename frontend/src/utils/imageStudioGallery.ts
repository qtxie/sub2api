import type { ImageStudioProvider } from '@/api/imageStudio'

export interface ImageStudioGalleryItem {
  id: string
  userId: number
  createdAt: number
  prompt: string
  revisedPrompt?: string
  apiKeyId: number
  provider: ImageStudioProvider
  model: string
  size: string
  aspectRatio?: string
  imageSize?: string
  resolution?: string
  quality?: string
  background?: string
  outputFormat: string
  imageSrc: string
}

const databaseName = 'sub2api-image-studio'
const databaseVersion = 3
const storeName = 'gallery'
const userIndexName = 'userId'
const safeDataMimes = ['image/png', 'image/jpeg', 'image/webp']

export function sanitizeImageStudioSource(value: unknown): string {
  if (typeof value !== 'string') return ''
  const source = value.trim()
  if (!source) return ''
  if (source.startsWith('data:')) {
    const comma = source.indexOf(',')
    if (comma < 0) return ''
    const header = source.slice(5, comma).toLowerCase()
    if (!safeDataMimes.some((mime) => header === `${mime};base64`)) return ''
    const payload = source.slice(comma + 1)
    if (!payload || payload.length % 4 === 1 || !/^[A-Za-z0-9+/]*={0,2}$/.test(payload)) return ''
    return source
  }
  try {
    const parsed = new URL(source)
    if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password) return ''
    return parsed.href
  } catch {
    return ''
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
    transaction.onabort = () => reject(transaction.error || new Error('Image gallery transaction was aborted'))
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
      if (!store.indexNames.contains(userIndexName)) store.createIndex(userIndexName, 'userId', { unique: false })
    }
    open.onsuccess = () => resolve(open.result)
    open.onerror = () => reject(open.error)
  })
}

export async function listImageStudioGallery(userId: number): Promise<ImageStudioGalleryItem[]> {
  const db = await openDatabase()
  if (!db) return []
  try {
    const transaction = db.transaction(storeName, 'readonly')
    const items = await request(transaction.objectStore(storeName).index(userIndexName).getAll(IDBKeyRange.only(userId))) as ImageStudioGalleryItem[]
    return items
      .map(normalizeGalleryItem)
      .filter((item) => item.imageSrc)
      .sort((a, b) => b.createdAt - a.createdAt)
  } finally {
    db.close()
  }
}

function normalizeGalleryItem(item: ImageStudioGalleryItem): ImageStudioGalleryItem {
  const provider = isImageStudioProvider(item.provider) ? item.provider : providerForLegacyModel(item.model)
  const aspectRatio = item.aspectRatio || legacyAspectRatio(provider, item.size)
  return {
    ...item,
    provider,
    size: item.size || aspectRatio || '1:1',
    ...(aspectRatio ? { aspectRatio } : {}),
    outputFormat: item.outputFormat || 'jpeg',
    imageSrc: sanitizeImageStudioSource(item.imageSrc)
  }
}

function legacyAspectRatio(provider: ImageStudioProvider, size: string): string | undefined {
  if (provider === 'openai') return undefined
  if (size?.includes(':')) return size
  if (size === '1536x1024') return '3:2'
  if (size === '1024x1536') return '2:3'
  return provider === 'gemini' ? '1:1' : undefined
}

function providerForLegacyModel(model: string): ImageStudioProvider {
  if (model?.startsWith('gemini-')) return 'gemini'
  if (model?.startsWith('grok-')) return 'grok'
  return 'openai'
}

function isImageStudioProvider(value: unknown): value is ImageStudioProvider {
  return value === 'openai' || value === 'gemini' || value === 'grok'
}

export async function saveImageStudioGalleryItem(item: ImageStudioGalleryItem): Promise<void> {
  const imageSrc = sanitizeImageStudioSource(item.imageSrc)
  if (!imageSrc) throw new Error('Unsafe image source')
  const db = await openDatabase()
  if (!db) return
  try {
    const transaction = db.transaction(storeName, 'readwrite')
    const done = transactionDone(transaction)
    transaction.objectStore(storeName).put({ ...item, imageSrc })
    await done
  } finally {
    db.close()
  }
}

export async function deleteImageStudioGalleryItem(userId: number, id: string): Promise<void> {
  const db = await openDatabase()
  if (!db) return
  try {
    const transaction = db.transaction(storeName, 'readwrite')
    const done = transactionDone(transaction)
    const store = transaction.objectStore(storeName)
    const item = await request(store.get(id)) as ImageStudioGalleryItem | undefined
    if (item?.userId === userId) store.delete(id)
    await done
  } finally {
    db.close()
  }
}

export async function clearImageStudioGallery(userId: number): Promise<void> {
  const db = await openDatabase()
  if (!db) return
  try {
    const transaction = db.transaction(storeName, 'readwrite')
    const done = transactionDone(transaction)
    const index = transaction.objectStore(storeName).index(userIndexName)
    await new Promise<void>((resolve, reject) => {
      const cursorRequest = index.openKeyCursor(IDBKeyRange.only(userId))
      cursorRequest.onerror = () => reject(cursorRequest.error)
      cursorRequest.onsuccess = () => {
        const cursor = cursorRequest.result
        if (!cursor) {
          resolve()
          return
        }
        transaction.objectStore(storeName).delete(cursor.primaryKey)
        cursor.continue()
      }
    })
    await done
  } finally {
    db.close()
  }
}
