export interface ImageStudioGalleryItem {
  id: string
  userId: number
  createdAt: number
  prompt: string
  revisedPrompt?: string
  apiKeyId: number
  size?: string
  quality?: string
  background?: string
  outputFormat?: string
  imageSrc: string
}

const databaseName = 'sub2api-image-studio'
const storeName = 'gallery'

function hasIndexedDB(): boolean {
  return typeof window !== 'undefined' && 'indexedDB' in window
}

function request<T>(value: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    value.onsuccess = () => resolve(value.result)
    value.onerror = () => reject(value.error)
  })
}

async function openDatabase(): Promise<IDBDatabase | null> {
  if (!hasIndexedDB()) return null
  return new Promise((resolve, reject) => {
    const open = window.indexedDB.open(databaseName, 1)
    open.onupgradeneeded = () => {
      if (!open.result.objectStoreNames.contains(storeName)) {
        open.result.createObjectStore(storeName, { keyPath: 'id' })
      }
    }
    open.onsuccess = () => resolve(open.result)
    open.onerror = () => reject(open.error)
  })
}

export async function listImageStudioGallery(userId: number): Promise<ImageStudioGalleryItem[]> {
  const db = await openDatabase()
  if (!db) return []
  try {
    const tx = db.transaction(storeName, 'readonly')
    const items = await request(tx.objectStore(storeName).getAll()) as ImageStudioGalleryItem[]
    return items
      .filter((item) => item.userId === userId)
      .sort((a, b) => b.createdAt - a.createdAt)
  } finally {
    db.close()
  }
}

export async function saveImageStudioGalleryItem(item: ImageStudioGalleryItem): Promise<void> {
  const db = await openDatabase()
  if (!db) return
  try {
    const tx = db.transaction(storeName, 'readwrite')
    await request(tx.objectStore(storeName).put(item))
  } finally {
    db.close()
  }
}

export async function deleteImageStudioGalleryItem(userId: number, id: string): Promise<void> {
  const db = await openDatabase()
  if (!db) return
  try {
    const tx = db.transaction(storeName, 'readwrite')
    const store = tx.objectStore(storeName)
    const item = await request(store.get(id)) as ImageStudioGalleryItem | undefined
    if (item?.userId === userId) await request(store.delete(id))
  } finally {
    db.close()
  }
}

export async function clearImageStudioGallery(userId: number): Promise<void> {
  const db = await openDatabase()
  if (!db) return
  try {
    const tx = db.transaction(storeName, 'readwrite')
    const store = tx.objectStore(storeName)
    const items = await request(store.getAll()) as ImageStudioGalleryItem[]
    await Promise.all(items
      .filter((item) => item.userId === userId)
      .map((item) => request(store.delete(item.id))))
  } finally {
    db.close()
  }
}
