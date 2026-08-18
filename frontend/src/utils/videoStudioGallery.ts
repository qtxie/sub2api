import type {
  VideoStudioAspectRatio,
  VideoStudioResolution,
  VideoStudioStatus
} from '@/api/videoStudio'
import {
  VIDEO_STUDIO_ASPECT_RATIOS,
  VIDEO_STUDIO_MODEL,
  VIDEO_STUDIO_RESOLUTIONS
} from '@/api/videoStudio'

export interface VideoStudioGalleryItem {
  id: string
  requestId: string
  userId: number
  apiKeyId: number
  createdAt: number
  updatedAt: number
  prompt: string
  model: typeof VIDEO_STUDIO_MODEL
  duration: number
  aspectRatio: VideoStudioAspectRatio
  resolution: VideoStudioResolution
  status: VideoStudioStatus
  trackingPaused: boolean
  progress?: number
  lastError?: string
  completedDuration?: number
  videoBlob?: Blob
}

const databaseName = 'sub2api-video-studio'
const databaseVersion = 1
const storeName = 'jobs'
const userIndexName = 'userId'
const maxTasksPerUser = 40
const maxBlobBytesPerUser = 512 * 1024 * 1024
const maxSingleBlobBytes = 192 * 1024 * 1024
const maxTaskAgeMs = 30 * 24 * 60 * 60 * 1000
export const VIDEO_STUDIO_TASK_BINDING_MS = 24 * 60 * 60 * 1000

export function videoStudioTaskID(userId: number, requestId: string): string {
  return `${userId}:${requestId.trim()}`
}

export async function listVideoStudioGallery(userId: number): Promise<VideoStudioGalleryItem[]> {
  const db = await openDatabase()
  if (!db) return []
  try {
    const transaction = db.transaction(storeName, 'readwrite')
    const done = transactionDone(transaction)
    const store = transaction.objectStore(storeName)
    const values = await request(store.index(userIndexName).getAll(IDBKeyRange.only(userId)))
    const items = enforceBounds(store, values as VideoStudioGalleryItem[])
    await done
    return items.sort((a, b) => b.createdAt - a.createdAt)
  } finally {
    db.close()
  }
}

export async function saveVideoStudioGalleryItem(item: VideoStudioGalleryItem): Promise<VideoStudioGalleryItem> {
  const normalized = normalizeVideoStudioGalleryItem(item)
  if (!normalized) throw new Error('Invalid Video Studio gallery item')
  if (normalized.videoBlob && normalized.videoBlob.size > maxSingleBlobBytes) delete normalized.videoBlob
  try {
    await persistItem(normalized)
  } catch (error) {
    if (!normalized.videoBlob) throw error
    delete normalized.videoBlob
    await persistItem(normalized)
  }
  return normalized
}

export async function deleteVideoStudioGalleryItem(userId: number, id: string): Promise<void> {
  const db = await openDatabase()
  if (!db) return
  try {
    const transaction = db.transaction(storeName, 'readwrite')
    const done = transactionDone(transaction)
    const store = transaction.objectStore(storeName)
    const item = await request(store.get(id)) as VideoStudioGalleryItem | undefined
    if (item?.userId === userId) store.delete(id)
    await done
  } finally {
    db.close()
  }
}

export async function clearVideoStudioGallery(userId: number): Promise<void> {
  const db = await openDatabase()
  if (!db) return
  try {
    const transaction = db.transaction(storeName, 'readwrite')
    const done = transactionDone(transaction)
    const store = transaction.objectStore(storeName)
    const index = store.index(userIndexName)
    await new Promise<void>((resolve, reject) => {
      const cursorRequest = index.openKeyCursor(IDBKeyRange.only(userId))
      cursorRequest.onerror = () => reject(cursorRequest.error)
      cursorRequest.onsuccess = () => {
        const cursor = cursorRequest.result
        if (!cursor) {
          resolve()
          return
        }
        store.delete(cursor.primaryKey)
        cursor.continue()
      }
    })
    await done
  } finally {
    db.close()
  }
}

export function normalizeVideoStudioGalleryItem(value: unknown): VideoStudioGalleryItem | null {
  if (!value || typeof value !== 'object') return null
  const item = value as Record<string, unknown>
  const requestId = stringValue(item.requestId)
  const userId = positiveInteger(item.userId)
  const apiKeyId = positiveInteger(item.apiKeyId)
  const createdAt = positiveNumber(item.createdAt)
  const updatedAt = positiveNumber(item.updatedAt) || createdAt
  const prompt = stringValue(item.prompt)
  const duration = boundedInteger(item.duration, 1, 15)
  const aspectRatio = enumValue(item.aspectRatio, VIDEO_STUDIO_ASPECT_RATIOS)
  const resolution = enumValue(item.resolution, VIDEO_STUDIO_RESOLUTIONS)
  const status = normalizeStatus(item.status)
  if (!requestId || !userId || !apiKeyId || !createdAt || !prompt || !duration || !aspectRatio || !resolution || !status) {
    return null
  }
  const blob = typeof Blob !== 'undefined' && item.videoBlob instanceof Blob && item.videoBlob.type === 'video/mp4'
    ? item.videoBlob
    : undefined
  const completedDuration = boundedInteger(item.completedDuration, 1, 15)
  const progress = clampedNumber(item.progress, 0, 100)
  return {
    id: videoStudioTaskID(userId, requestId),
    requestId,
    userId,
    apiKeyId,
    createdAt,
    updatedAt: updatedAt || createdAt,
    prompt,
    model: VIDEO_STUDIO_MODEL,
    duration,
    aspectRatio,
    resolution,
    status,
    trackingPaused: item.trackingPaused === true,
    ...(progress !== null ? { progress } : {}),
    ...(stringValue(item.lastError) ? { lastError: stringValue(item.lastError) } : {}),
    ...(completedDuration ? { completedDuration } : {}),
    ...(blob ? { videoBlob: blob } : {})
  }
}

export function expireStaleVideoStudioTasks(
  items: VideoStudioGalleryItem[],
  now = Date.now()
): { items: VideoStudioGalleryItem[]; expired: VideoStudioGalleryItem[] } {
  const expired: VideoStudioGalleryItem[] = []
  const nextItems = items.map((item) => {
    if (item.status !== 'pending' || now - item.createdAt < VIDEO_STUDIO_TASK_BINDING_MS) return item
    const next: VideoStudioGalleryItem = {
      ...item,
      status: 'expired',
      trackingPaused: true,
      updatedAt: now
    }
    delete next.progress
    expired.push(next)
    return next
  })
  return { items: nextItems, expired }
}

async function persistItem(item: VideoStudioGalleryItem): Promise<void> {
  const db = await openDatabase()
  if (!db) return
  try {
    const transaction = db.transaction(storeName, 'readwrite')
    const done = transactionDone(transaction)
    const store = transaction.objectStore(storeName)
    store.put(item)
    const all = await request(store.index(userIndexName).getAll(IDBKeyRange.only(item.userId))) as VideoStudioGalleryItem[]
    enforceBounds(store, all)
    await done
  } finally {
    db.close()
  }
}

function enforceBounds(store: IDBObjectStore, values: VideoStudioGalleryItem[]): VideoStudioGalleryItem[] {
  const now = Date.now()
  const items = values
    .map(normalizeVideoStudioGalleryItem)
    .filter((item): item is VideoStudioGalleryItem => item !== null)
    .sort((a, b) => b.updatedAt - a.updatedAt)
  let blobBytes = 0

  const retained: VideoStudioGalleryItem[] = []
  for (const [index, item] of items.entries()) {
    if (index >= maxTasksPerUser || now - item.updatedAt > maxTaskAgeMs) {
      store.delete(item.id)
      continue
    }
    if (!item.videoBlob) {
      retained.push(item)
      continue
    }
    if (blobBytes + item.videoBlob.size > maxBlobBytesPerUser) {
      const metadataOnly = { ...item }
      delete metadataOnly.videoBlob
      store.put(metadataOnly)
      retained.push(metadataOnly)
      continue
    }
    blobBytes += item.videoBlob.size
    retained.push(item)
  }
  return retained
}

function hasIndexedDB(): boolean {
  return typeof window !== 'undefined' && 'indexedDB' in window
}

function openDatabase(): Promise<IDBDatabase | null> {
  if (!hasIndexedDB()) return Promise.resolve(null)
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
    transaction.onabort = () => reject(transaction.error || new Error('Video Studio transaction was aborted'))
  })
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function positiveInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : null
}

function positiveNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null
}

function clampedNumber(value: unknown, min: number, max: number): number | null {
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.min(max, Math.max(min, value))
    : null
}

function boundedInteger(value: unknown, min: number, max: number): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value >= min && value <= max ? value : null
}

function enumValue<T extends string>(value: unknown, allowed: readonly T[]): T | null {
  const normalized = stringValue(value)
  return allowed.includes(normalized as T) ? normalized as T : null
}

function normalizeStatus(value: unknown): VideoStudioStatus | null {
  const status = stringValue(value)
  return status === 'pending' || status === 'done' || status === 'failed' || status === 'expired' ? status : null
}
