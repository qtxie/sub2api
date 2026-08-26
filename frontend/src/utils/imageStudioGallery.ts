import type { ImageStudioProvider } from '@/api/imageStudio'

export interface ImageStudioGalleryConfig {
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
  count?: number
  resultIndex?: number
  sourceImageCount?: number
  isEdit?: boolean
}

/** A full-resolution result that exists only for the current browser session. */
export interface ImageStudioGalleryItem extends ImageStudioGalleryConfig {
  imageSrc: string
}

const sessionResultsByUser = new Map<number, ImageStudioGalleryItem[]>()

/** Full-resolution results are retained for the active SPA session only. */
export function getImageStudioSessionResults(userId: number): ImageStudioGalleryItem[] {
  if (!positiveInteger(userId)) return []
  return [...(sessionResultsByUser.get(userId) || [])]
}

export function setImageStudioSessionResults(userId: number, items: ImageStudioGalleryItem[]): void {
  if (!positiveInteger(userId)) return
  sessionResultsByUser.set(userId, [...items])
}

/** The only Image Studio record shape allowed in IndexedDB from database version 4. */
export interface ImageStudioArchiveItem extends ImageStudioGalleryConfig {
  recordVersion: 4
  archivedAt: number
  thumbnailSrc?: string
  thumbnailUnavailable?: true
}

const databaseName = 'sub2api-image-studio'
const databaseVersion = 5
const storeName = 'gallery'
const metadataStoreName = 'metadata'
const userIndexName = 'userId'
const userCreatedAtIndexName = 'userCreatedAt'
const recordVersionIndexName = 'recordVersion'
const migrationMarkerKey = 'archive-schema-v4'
const archiveOperationLockName = 'sub2api-image-studio-archive-schema-v4'
const safeDataMimes = ['image/png', 'image/jpeg', 'image/webp'] as const
const thumbnailMaxEdge = 360
const thumbnailMaxBytes = 256 * 1024
const thumbnailInputMaxBytes = 64 * 1024 * 1024
const maxArchivesPerUser = 500
const maxArchiveBytesPerUser = 48 * 1024 * 1024
const maxArchivesGlobal = 1000
const maxArchiveBytesGlobal = 96 * 1024 * 1024
const archiveRecordSafetyBytes = 2 * 1024
const archiveLimitErrorCode = 'IMAGE_STUDIO_ARCHIVE_LIMIT'
let archiveMigrationPromise: Promise<void> | null = null

class ImageStudioArchiveLimitError extends Error {
  readonly code = archiveLimitErrorCode

  constructor(message: string) {
    super(message)
    this.name = 'ImageStudioArchiveLimitError'
  }
}

export function isImageStudioArchiveLimitError(error: unknown): boolean {
  if (error instanceof ImageStudioArchiveLimitError) return true
  if (!error || typeof error !== 'object') return false
  const value = error as Record<string, unknown>
  return value.name === 'ImageStudioArchiveLimitError'
    || value.name === 'QuotaExceededError'
    || value.code === archiveLimitErrorCode
}

export function sanitizeImageStudioSource(value: unknown): string {
  if (typeof value !== 'string') return ''
  const source = value.trim()
  if (!source) return ''
  if (source.startsWith('data:')) return parseRasterDataURL(source)?.source || ''
  try {
    const parsed = new URL(source)
    if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password) return ''
    return parsed.href
  } catch {
    return ''
  }
}

export async function createImageStudioThumbnail(imageSrc: string): Promise<string> {
  const source = sanitizeImageStudioSource(imageSrc)
  if (!source) throw new Error('Unsafe image source')
  const blob = await sourceBlob(source)
  if (!safeDataMimes.includes(blob.type.toLowerCase() as typeof safeDataMimes[number])) {
    throw new Error('Unsupported image type')
  }
  if (blob.size > thumbnailInputMaxBytes) throw new Error('Image is too large to archive')

  const decoded = await loadThumbnailSource(blob)
  try {
    if (!Number.isFinite(decoded.width) || !Number.isFinite(decoded.height) || decoded.width <= 0 || decoded.height <= 0) {
      throw new Error('Image has invalid dimensions')
    }
    const initialScale = Math.min(1, thumbnailMaxEdge / Math.max(decoded.width, decoded.height))
    const canvas = document.createElement('canvas')
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Canvas is unavailable')

    const dimensionScales = [1, 0.8, 0.65, 0.5, 0.4, 0.3, 0.2]
    const qualities = [0.72, 0.58, 0.44, 0.32]
    for (const dimensionScale of dimensionScales) {
      canvas.width = Math.max(1, Math.round(decoded.width * initialScale * dimensionScale))
      canvas.height = Math.max(1, Math.round(decoded.height * initialScale * dimensionScale))
      context.clearRect(0, 0, canvas.width, canvas.height)
      context.drawImage(decoded.image, 0, 0, canvas.width, canvas.height)

      const webp = await firstThumbnailCandidate(canvas, 'image/webp', qualities)
      if (webp) return blobDataURL(webp)
      const jpeg = await firstThumbnailCandidate(canvas, 'image/jpeg', qualities)
      if (jpeg) return blobDataURL(jpeg)
      const png = await canvasBlob(canvas, 'image/png')
      if (png && png.size > 0 && png.size <= thumbnailMaxBytes) return blobDataURL(png)
    }
    throw new Error('Thumbnail exceeds the archive size limit')
  } finally {
    decoded.close()
  }
}

export function createImageStudioArchiveItem(
  item: ImageStudioGalleryItem,
  thumbnailSrc: string,
  archivedAt = Date.now()
): ImageStudioArchiveItem {
  const config = normalizeGalleryConfig(item)
  if (!config || !sanitizeImageStudioSource(item.imageSrc)) throw new Error('Invalid Image Studio gallery item')
  const archiveTime = positiveNumber(archivedAt)
  if (!archiveTime) throw new Error('Invalid archive timestamp')
  validateExplicitThumbnail(thumbnailSrc)
  const thumbnail = normalizeThumbnailSource(thumbnailSrc)
  return archiveFromConfig(config, thumbnail, archiveTime)
}

/** Conservative IndexedDB footprint estimate for a canonical archive record. */
export function estimateImageStudioArchiveStorageBytes(item: ImageStudioArchiveItem): number {
  const normalized = normalizeArchiveRecord(item)
  if (!normalized) throw new Error('Invalid Image Studio archive item')
  return Math.ceil(JSON.stringify(normalized).length * 2.25) + archiveRecordSafetyBytes
}

export async function listImageStudioGallery(userId: number): Promise<ImageStudioArchiveItem[]> {
  if (!positiveInteger(userId)) return []
  return await withArchiveOperationLock(async () => {
    const db = await openDatabase()
    if (!db) return []
    try {
      await ensureArchiveMigration(db)
      return await readUserArchives(db, userId)
    } finally {
      db.close()
    }
  })
}

export async function saveImageStudioGalleryItem(item: ImageStudioArchiveItem): Promise<void> {
  validateExplicitThumbnail(item.thumbnailSrc)
  const normalized = normalizeArchiveRecord(item)
  if (!normalized) throw new Error('Invalid Image Studio archive item')
  await withArchiveOperationLock(async () => {
    const db = await openDatabase()
    if (!db) return
    try {
      await ensureArchiveMigration(db)
      await persistArchiveWithinLimits(db, normalized)
    } finally {
      db.close()
    }
  })
}

export async function deleteImageStudioGalleryItem(userId: number, id: string): Promise<void> {
  await withArchiveOperationLock(async () => {
    const db = await openDatabase()
    if (!db) return
    try {
      await ensureArchiveMigration(db)
      const transaction = db.transaction(storeName, 'readwrite')
      const done = transactionDone(transaction)
      const store = transaction.objectStore(storeName)
      const item = await request(store.get(id)) as Record<string, unknown> | undefined
      if (item?.userId === userId) store.delete(id)
      await done
    } finally {
      db.close()
    }
  })
}

export async function clearImageStudioGallery(userId: number): Promise<void> {
  await withArchiveOperationLock(async () => {
    const db = await openDatabase()
    if (!db) return
    try {
      await ensureArchiveMigration(db)
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
  })
}

function normalizeGalleryConfig(value: unknown): ImageStudioGalleryConfig | null {
  if (!value || typeof value !== 'object') return null
  const item = value as Record<string, unknown>
  const id = boundedString(item.id, 256)
  const userId = positiveInteger(item.userId)
  const createdAt = positiveNumber(item.createdAt)
  const prompt = boundedString(item.prompt, 32 * 1024)
  const apiKeyId = positiveInteger(item.apiKeyId)
  const model = boundedString(item.model, 256)
  if (!id || !userId || !createdAt || !prompt || !apiKeyId || !model) return null

  const provider = isImageStudioProvider(item.provider) ? item.provider : providerForLegacyModel(model)
  const rawSize = boundedString(item.size, 128)
  const legacyRatio = legacyAspectRatio(provider, rawSize)
  const aspectRatio = boundedString(item.aspectRatio, 64) || legacyRatio
  const revisedPrompt = boundedString(item.revisedPrompt, 32 * 1024)
  const imageSize = boundedString(item.imageSize, 64)
  const resolution = boundedString(item.resolution, 64)
  const quality = boundedString(item.quality, 64)
  const background = boundedString(item.background, 64)
  const count = positiveInteger(item.count)
  const resultIndex = nonNegativeInteger(item.resultIndex)
  const sourceImageCount = nonNegativeInteger(item.sourceImageCount)
  const isEdit = typeof item.isEdit === 'boolean' ? item.isEdit : null
  return {
    id,
    userId,
    createdAt,
    prompt,
    ...(revisedPrompt ? { revisedPrompt } : {}),
    apiKeyId,
    provider,
    model,
    size: rawSize || aspectRatio || '1:1',
    ...(aspectRatio ? { aspectRatio } : {}),
    ...(imageSize ? { imageSize } : {}),
    ...(resolution ? { resolution } : {}),
    ...(quality ? { quality } : {}),
    ...(background ? { background } : {}),
    outputFormat: normalizeOutputFormat(item.outputFormat),
    ...(count ? { count } : {}),
    ...(resultIndex !== null ? { resultIndex } : {}),
    ...(sourceImageCount !== null ? { sourceImageCount } : {}),
    ...(isEdit !== null ? { isEdit } : {})
  }
}

function normalizeArchiveRecord(value: unknown): ImageStudioArchiveItem | null {
  if (!value || typeof value !== 'object') return null
  const item = value as Record<string, unknown>
  if (item.recordVersion !== 4) return null
  const config = normalizeGalleryConfig(item)
  if (!config) return null
  const archivedAt = positiveNumber(item.archivedAt) || config.createdAt
  const thumbnail = normalizeThumbnailSource(item.thumbnailSrc)
  return archiveFromConfig(config, thumbnail, archivedAt)
}

function isCanonicalArchiveRecord(value: unknown): value is ImageStudioArchiveItem {
  const normalized = normalizeArchiveRecord(value)
  return Boolean(normalized && flatRecordsEqual(value as Record<string, unknown>, normalized as unknown as Record<string, unknown>))
}

function archiveFromConfig(
  config: ImageStudioGalleryConfig,
  thumbnailSrc: string,
  archivedAt: number
): ImageStudioArchiveItem {
  return {
    ...config,
    recordVersion: 4,
    archivedAt,
    ...(thumbnailSrc ? { thumbnailSrc } : { thumbnailUnavailable: true as const })
  }
}

function normalizeThumbnailSource(value: unknown): string {
  if (typeof value !== 'string' || !value.trim()) return ''
  const parsed = parseRasterDataURL(value.trim())
  if (!parsed || parsed.bytes <= 0 || parsed.bytes > thumbnailMaxBytes || !hasRasterSignature(parsed)) return ''
  return parsed.source
}

function validateExplicitThumbnail(value: unknown): void {
  if (value === undefined || value === '') return
  if (typeof value !== 'string') throw new Error('Invalid Image Studio thumbnail')
  const parsed = parseRasterDataURL(value.trim())
  if (!parsed || parsed.bytes <= 0) throw new Error('Invalid Image Studio thumbnail')
  if (parsed.bytes > thumbnailMaxBytes) {
    throw new ImageStudioArchiveLimitError('Image Studio thumbnail exceeds the storage limit')
  }
  if (!hasRasterSignature(parsed)) throw new Error('Invalid Image Studio thumbnail')
}

function normalizeOutputFormat(value: unknown): string {
  const format = boundedString(value, 16).toLowerCase()
  if (format === 'jpg') return 'jpeg'
  return format === 'png' || format === 'jpeg' || format === 'webp' ? format : 'jpeg'
}

function legacyAspectRatio(provider: ImageStudioProvider, size: string): string | undefined {
  if (provider === 'openai') return undefined
  if (size.includes(':')) return size
  if (size === '1536x1024') return '3:2'
  if (size === '1024x1536') return '2:3'
  return provider === 'gemini' ? '1:1' : undefined
}

function providerForLegacyModel(model: string): ImageStudioProvider {
  if (model.startsWith('gemini-')) return 'gemini'
  if (model.startsWith('grok-')) return 'grok'
  return 'openai'
}

function isImageStudioProvider(value: unknown): value is ImageStudioProvider {
  return value === 'openai' || value === 'gemini' || value === 'grok'
}

function ensureArchiveMigration(db: IDBDatabase): Promise<void> {
  if (!archiveMigrationPromise) {
    archiveMigrationPromise = migrateArchiveStoreIfNeeded(db).catch((error) => {
      archiveMigrationPromise = null
      throw error
    })
  }
  return archiveMigrationPromise
}

async function migrateArchiveStoreIfNeeded(db: IDBDatabase): Promise<void> {
  if (await hasArchiveMigrationMarker(db)) return
  await enforceMigrationCountBounds(db)
  for (let attempt = 0; attempt < 3; attempt += 1) {
    const keys = await allRecordKeys(db)
    for (const key of keys) await migrateRecord(db, key)
    await enforceMigrationCountBounds(db)
    await enforceMigrationStorageBounds(db)
    if (await markMigrationCompleteAfterAudit(db)) return
  }
  throw new Error('Image Studio archive migration could not reach a canonical state')
}

async function migrateRecord(db: IDBDatabase, key: IDBValidKey): Promise<void> {
  const raw = await readRecord(db, key)
  if (!raw) return
  if (isCanonicalArchiveRecord(raw)) return

  const existingArchive = normalizeArchiveRecord(raw)
  if (existingArchive) {
    await persistMigratedRecord(db, key, raw, existingArchive)
    return
  }

  const config = normalizeGalleryConfig(raw)
  if (!config) {
    await mutateRecordIfUnchanged(db, key, raw, null)
    return
  }

  const imageSrc = sanitizeImageStudioSource(raw.imageSrc)
  const thumbnail = imageSrc ? await createImageStudioThumbnail(imageSrc).catch(() => '') : ''
  const archivedAt = positiveNumber(raw.archivedAt) || config.createdAt
  await persistMigratedRecord(db, key, raw, archiveFromConfig(config, thumbnail, archivedAt))
}

async function persistMigratedRecord(
  db: IDBDatabase,
  key: IDBValidKey,
  raw: Record<string, unknown>,
  archive: ImageStudioArchiveItem
): Promise<void> {
  try {
    await mutateRecordIfUnchanged(db, key, raw, archive)
  } catch {
    const metadataOnly = archiveFromConfig(normalizeGalleryConfig(archive)!, '', archive.archivedAt)
    try {
      await mutateRecordIfUnchanged(db, key, raw, metadataOnly)
    } catch {
      await mutateRecordIfUnchanged(db, key, raw, null)
    }
  }
}

function enforceMigrationCountBounds(db: IDBDatabase): Promise<void> {
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(storeName, 'readwrite')
    const store = transaction.objectStore(storeName)
    const index = store.index(userCreatedAtIndexName)
    const entries: CreatedRecordEntry[] = []
    let failure: unknown
    const cursorRequest = index.openKeyCursor()
    cursorRequest.onerror = () => { failure = cursorRequest.error }
    cursorRequest.onsuccess = () => {
      const cursor = cursorRequest.result
      if (cursor) {
        const compoundKey = cursor.key
        const id = typeof cursor.primaryKey === 'string' ? cursor.primaryKey : ''
        if (id && Array.isArray(compoundKey) && positiveInteger(compoundKey[0]) && positiveNumber(compoundKey[1])) {
          entries.push({ id, userId: compoundKey[0] as number, createdAt: compoundKey[1] as number })
        }
        cursor.continue()
        return
      }

      const deleteIDs = archiveCountOverflowIDs(entries)
      for (const id of deleteIDs) {
        const deletion = store.delete(id)
        deletion.onerror = () => { failure = deletion.error }
      }
    }
    transaction.oncomplete = () => resolve()
    transaction.onerror = () => { failure ||= transaction.error }
    transaction.onabort = () => reject(toArchiveStorageError(failure || transaction.error, 'Image Studio archive count cleanup failed'))
  })
}

function archiveCountOverflowIDs(entries: CreatedRecordEntry[]): Set<string> {
  const deleteIDs = new Set<string>()
  const byUser = new Map<number, CreatedRecordEntry[]>()
  for (const entry of entries) {
    const userEntries = byUser.get(entry.userId) || []
    userEntries.push(entry)
    byUser.set(entry.userId, userEntries)
  }
  for (const userEntries of byUser.values()) {
    userEntries.sort(compareCreatedRecords)
    for (const entry of userEntries.slice(0, Math.max(0, userEntries.length - maxArchivesPerUser))) {
      deleteIDs.add(entry.id)
    }
  }
  const retained = entries.filter((entry) => !deleteIDs.has(entry.id)).sort(compareCreatedRecords)
  for (const entry of retained.slice(0, Math.max(0, retained.length - maxArchivesGlobal))) {
    deleteIDs.add(entry.id)
  }
  return deleteIDs
}

function enforceMigrationStorageBounds(db: IDBDatabase): Promise<void> {
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(storeName, 'readwrite')
    const store = transaction.objectStore(storeName)
    const index = store.index(recordVersionIndexName)
    const entries: ArchiveStorageEntry[] = []
    let failure: unknown
    const cursorRequest = index.openCursor(IDBKeyRange.only(4))
    cursorRequest.onerror = () => { failure = cursorRequest.error }
    cursorRequest.onsuccess = () => {
      const cursor = cursorRequest.result
      if (cursor) {
        const archive = normalizeArchiveRecord(cursor.value)
        if (archive) entries.push(archiveStorageEntry(archive))
        cursor.continue()
        return
      }

      const actions = archiveStorageActions(entries)
      for (const entry of entries) {
        const action = actions.get(entry.id)
        if (action === 'delete') {
          const deletion = store.delete(entry.id)
          deletion.onerror = () => { failure = deletion.error }
        } else if (action === 'strip') {
          const put = store.put(entry.metadataOnly)
          put.onerror = () => { failure = put.error }
        }
      }
    }
    transaction.oncomplete = () => resolve()
    transaction.onerror = () => { failure ||= transaction.error }
    transaction.onabort = () => reject(toArchiveStorageError(failure || transaction.error, 'Image Studio archive storage cleanup failed'))
  })
}

function persistArchiveWithinLimits(db: IDBDatabase, item: ImageStudioArchiveItem): Promise<void> {
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(storeName, 'readwrite')
    const store = transaction.objectStore(storeName)
    const userIndex = store.index(userIndexName)
    const versionIndex = store.index(recordVersionIndexName)
    let failure: Error | null = null
    let existing: Record<string, unknown> | undefined
    let globalCount = 0
    let userCount = 0
    let globalBytes = 0
    let userBytes = 0
    let existingBytes = 0
    let completed = 0
    let putStarted = false

    const fail = (error: Error) => {
      if (failure) return
      failure = error
      try {
        transaction.abort()
      } catch {
        reject(error)
      }
    }
    const maybePut = () => {
      completed += 1
      if (completed !== 4 || failure || putStarted) return
      if (existing && existing.userId !== item.userId) {
        fail(new Error('Archive ID belongs to another user'))
        return
      }
      const addsRecord = !existing
      const projectedGlobalCount = globalCount + (addsRecord ? 1 : 0)
      const projectedUserCount = userCount + (addsRecord ? 1 : 0)
      const candidateBytes = estimateImageStudioArchiveStorageBytes(item)
      const projectedGlobalBytes = globalBytes - existingBytes + candidateBytes
      const projectedUserBytes = userBytes - existingBytes + candidateBytes
      if (projectedUserCount > maxArchivesPerUser || projectedGlobalCount > maxArchivesGlobal) {
        fail(new ImageStudioArchiveLimitError('Image Studio archive count limit reached'))
        return
      }
      if (projectedUserBytes > maxArchiveBytesPerUser || projectedGlobalBytes > maxArchiveBytesGlobal) {
        fail(new ImageStudioArchiveLimitError('Image Studio archive storage limit reached'))
        return
      }
      putStarted = true
      const put = store.put(item)
      put.onerror = () => fail(toArchiveStorageError(put.error, 'Failed to save Image Studio archive'))
    }

    const existingRequest = store.get(item.id)
    existingRequest.onsuccess = () => {
      existing = existingRequest.result as Record<string, unknown> | undefined
      maybePut()
    }
    existingRequest.onerror = () => fail(existingRequest.error || new Error('Failed to read Image Studio archive'))

    const globalCountRequest = store.count()
    globalCountRequest.onsuccess = () => {
      globalCount = globalCountRequest.result
      maybePut()
    }
    globalCountRequest.onerror = () => fail(globalCountRequest.error || new Error('Failed to count Image Studio archives'))

    const userCountRequest = userIndex.count(IDBKeyRange.only(item.userId))
    userCountRequest.onsuccess = () => {
      userCount = userCountRequest.result
      maybePut()
    }
    userCountRequest.onerror = () => fail(userCountRequest.error || new Error('Failed to count Image Studio archives'))

    const cursorRequest = versionIndex.openCursor(IDBKeyRange.only(4))
    cursorRequest.onerror = () => fail(cursorRequest.error || new Error('Failed to measure Image Studio archives'))
    cursorRequest.onsuccess = () => {
      const cursor = cursorRequest.result
      if (!cursor) {
        maybePut()
        return
      }
      const archive = normalizeArchiveRecord(cursor.value)
      if (archive) {
        const bytes = estimateImageStudioArchiveStorageBytes(archive)
        globalBytes += bytes
        if (archive.userId === item.userId) userBytes += bytes
        if (archive.id === item.id) existingBytes = bytes
      }
      cursor.continue()
    }

    transaction.oncomplete = () => resolve()
    transaction.onerror = () => {
      if (!failure) failure = toArchiveStorageError(transaction.error, 'Image Studio archive transaction failed')
    }
    transaction.onabort = () => reject(failure || toArchiveStorageError(transaction.error, 'Image Studio archive transaction was aborted'))
  })
}

interface CreatedRecordEntry {
  id: string
  userId: number
  createdAt: number
}

interface ArchiveStorageEntry {
  id: string
  userId: number
  createdAt: number
  archivedAt: number
  archive: ImageStudioArchiveItem
  metadataOnly: ImageStudioArchiveItem
  bytes: number
  metadataBytes: number
}

type ArchiveStorageAction = 'strip' | 'delete'

function archiveStorageEntry(archive: ImageStudioArchiveItem): ArchiveStorageEntry {
  const config = normalizeGalleryConfig(archive)!
  const metadataOnly = archiveFromConfig(config, '', archive.archivedAt)
  return {
    id: archive.id,
    userId: archive.userId,
    createdAt: archive.createdAt,
    archivedAt: archive.archivedAt,
    archive,
    metadataOnly,
    bytes: estimateImageStudioArchiveStorageBytes(archive),
    metadataBytes: estimateImageStudioArchiveStorageBytes(metadataOnly)
  }
}

function archiveStorageActions(entries: ArchiveStorageEntry[]): Map<string, ArchiveStorageAction> {
  const actions = new Map<string, ArchiveStorageAction>()
  const byUser = new Map<number, ArchiveStorageEntry[]>()
  for (const entry of entries) {
    const userEntries = byUser.get(entry.userId) || []
    userEntries.push(entry)
    byUser.set(entry.userId, userEntries)
  }
  for (const userEntries of byUser.values()) {
    reduceArchiveStorage(userEntries, actions, maxArchiveBytesPerUser)
  }
  reduceArchiveStorage(entries, actions, maxArchiveBytesGlobal)
  return actions
}

function reduceArchiveStorage(
  entries: ArchiveStorageEntry[],
  actions: Map<string, ArchiveStorageAction>,
  limit: number
): void {
  const sorted = [...entries].sort(compareArchiveStorageEntries)
  let bytes = sorted.reduce((total, entry) => total + archiveEntryBytes(entry, actions.get(entry.id)), 0)
  for (const entry of sorted) {
    if (bytes <= limit) return
    if (actions.has(entry.id) || !entry.archive.thumbnailSrc || entry.bytes <= entry.metadataBytes) continue
    actions.set(entry.id, 'strip')
    bytes -= entry.bytes - entry.metadataBytes
  }
  for (const entry of sorted) {
    if (bytes <= limit) return
    const action = actions.get(entry.id)
    if (action === 'delete') continue
    bytes -= archiveEntryBytes(entry, action)
    actions.set(entry.id, 'delete')
  }
}

function archiveEntryBytes(entry: ArchiveStorageEntry, action?: ArchiveStorageAction): number {
  if (action === 'delete') return 0
  return action === 'strip' ? entry.metadataBytes : entry.bytes
}

function migrationStateMatches(initial: Record<string, unknown>, current: Record<string, unknown>): boolean {
  const initialConfig = normalizeGalleryConfig(initial)
  const currentConfig = normalizeGalleryConfig(current)
  if (Boolean(initialConfig) !== Boolean(currentConfig)) return false
  if (initialConfig && currentConfig && !flatRecordsEqual(
    initialConfig as unknown as Record<string, unknown>,
    currentConfig as unknown as Record<string, unknown>
  )) return false
  const fields = ['id', 'userId', 'recordVersion', 'archivedAt', 'imageSrc', 'thumbnailSrc', 'thumbnailUnavailable']
  return fields.every((field) => Object.is(initial[field], current[field]))
}

function flatRecordsEqual(left: Record<string, unknown>, right: Record<string, unknown>): boolean {
  const leftKeys = Object.keys(left).sort()
  const rightKeys = Object.keys(right).sort()
  return leftKeys.length === rightKeys.length
    && leftKeys.every((key, index) => key === rightKeys[index] && Object.is(left[key], right[key]))
}

function mutateRecordIfUnchanged(
  db: IDBDatabase,
  key: IDBValidKey,
  initial: Record<string, unknown>,
  replacement: ImageStudioArchiveItem | null
): Promise<boolean> {
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(storeName, 'readwrite')
    const store = transaction.objectStore(storeName)
    let mutated = false
    let failure: unknown
    const get = store.get(key)
    get.onerror = () => { failure = get.error }
    get.onsuccess = () => {
      const current = get.result as Record<string, unknown> | undefined
      if (!current || isCanonicalArchiveRecord(current) || !migrationStateMatches(initial, current)) return
      const mutation = replacement ? store.put(replacement) : store.delete(key)
      mutated = true
      mutation.onerror = () => { failure = mutation.error }
    }
    transaction.oncomplete = () => resolve(mutated)
    transaction.onerror = () => { failure ||= transaction.error }
    transaction.onabort = () => reject(toArchiveStorageError(failure || transaction.error, 'Image Studio archive migration write failed'))
  })
}

function compareCreatedRecords(a: CreatedRecordEntry, b: CreatedRecordEntry): number {
  return a.createdAt - b.createdAt || a.id.localeCompare(b.id)
}

function compareArchiveStorageEntries(a: ArchiveStorageEntry, b: ArchiveStorageEntry): number {
  return a.archivedAt - b.archivedAt || a.createdAt - b.createdAt || a.id.localeCompare(b.id)
}

async function allRecordKeys(db: IDBDatabase): Promise<IDBValidKey[]> {
  const transaction = db.transaction(storeName, 'readonly')
  return await request(transaction.objectStore(storeName).getAllKeys())
}

async function hasArchiveMigrationMarker(db: IDBDatabase): Promise<boolean> {
  const transaction = db.transaction(metadataStoreName, 'readonly')
  const marker = await request(transaction.objectStore(metadataStoreName).get(migrationMarkerKey)) as Record<string, unknown> | undefined
  return marker?.key === migrationMarkerKey && marker?.recordVersion === 4
}

function markMigrationCompleteAfterAudit(db: IDBDatabase): Promise<boolean> {
  return new Promise((resolve, reject) => {
    const transaction = db.transaction([storeName, metadataStoreName], 'readwrite')
    const store = transaction.objectStore(storeName)
    const metadata = transaction.objectStore(metadataStoreName)
    const byUser = new Map<number, { count: number; bytes: number }>()
    let globalCount = 0
    let globalBytes = 0
    let auditPassed = true
    let markerWritten = false
    let failure: unknown
    const cursorRequest = store.openCursor()
    cursorRequest.onerror = () => { failure = cursorRequest.error }
    cursorRequest.onsuccess = () => {
      const cursor = cursorRequest.result
      if (cursor) {
        const value = cursor.value
        if (!isCanonicalArchiveRecord(value)) {
          auditPassed = false
        } else {
          const bytes = estimateImageStudioArchiveStorageBytes(value)
          globalCount += 1
          globalBytes += bytes
          const user = byUser.get(value.userId) || { count: 0, bytes: 0 }
          user.count += 1
          user.bytes += bytes
          byUser.set(value.userId, user)
        }
        cursor.continue()
        return
      }

      auditPassed = auditPassed
        && globalCount <= maxArchivesGlobal
        && globalBytes <= maxArchiveBytesGlobal
        && [...byUser.values()].every((user) => user.count <= maxArchivesPerUser && user.bytes <= maxArchiveBytesPerUser)
      if (!auditPassed) return
      const put = metadata.put({ key: migrationMarkerKey, recordVersion: 4, completedAt: Date.now() })
      markerWritten = true
      put.onerror = () => { failure = put.error }
    }
    transaction.oncomplete = () => resolve(auditPassed && markerWritten)
    transaction.onerror = () => { failure ||= transaction.error }
    transaction.onabort = () => reject(toArchiveStorageError(failure || transaction.error, 'Image Studio archive migration audit failed'))
  })
}

async function readUserArchives(db: IDBDatabase, userId: number): Promise<ImageStudioArchiveItem[]> {
  return await new Promise((resolve, reject) => {
    const transaction = db.transaction(storeName, 'readonly')
    const index = transaction.objectStore(storeName).index(userIndexName)
    const archives: ImageStudioArchiveItem[] = []
    const cursorRequest = index.openCursor(IDBKeyRange.only(userId))
    cursorRequest.onerror = () => reject(cursorRequest.error)
    cursorRequest.onsuccess = () => {
      const cursor = cursorRequest.result
      if (!cursor) {
        resolve(archives.sort((a, b) => b.archivedAt - a.archivedAt || b.createdAt - a.createdAt))
        return
      }
      const archive = normalizeArchiveRecord(cursor.value)
      if (archive) archives.push(archive)
      cursor.continue()
    }
  })
}

async function readRecord(db: IDBDatabase, key: IDBValidKey): Promise<Record<string, unknown> | undefined> {
  const transaction = db.transaction(storeName, 'readonly')
  return await request(transaction.objectStore(storeName).get(key)) as Record<string, unknown> | undefined
}

function hasIndexedDB(): boolean {
  return typeof window !== 'undefined' && 'indexedDB' in window
}

function withArchiveOperationLock<T>(operation: () => Promise<T>): Promise<T> {
  if (typeof navigator === 'undefined' || !navigator.locks?.request) return operation()
  return navigator.locks.request(archiveOperationLockName, { mode: 'exclusive' }, operation)
}

function openDatabase(): Promise<IDBDatabase | null> {
  if (!hasIndexedDB()) return Promise.resolve(null)
  return new Promise((resolve, reject) => {
    let settled = false
    const open = window.indexedDB.open(databaseName, databaseVersion)
    open.onupgradeneeded = () => {
      const store = open.result.objectStoreNames.contains(storeName)
        ? open.transaction!.objectStore(storeName)
        : open.result.createObjectStore(storeName, { keyPath: 'id' })
      if (!store.indexNames.contains(userIndexName)) store.createIndex(userIndexName, 'userId', { unique: false })
      if (!store.indexNames.contains(userCreatedAtIndexName)) {
        store.createIndex(userCreatedAtIndexName, ['userId', 'createdAt'], { unique: false })
      }
      if (!store.indexNames.contains(recordVersionIndexName)) {
        store.createIndex(recordVersionIndexName, 'recordVersion', { unique: false })
      }
      if (!open.result.objectStoreNames.contains(metadataStoreName)) {
        open.result.createObjectStore(metadataStoreName, { keyPath: 'key' })
      }
    }
    open.onsuccess = () => {
      const db = open.result
      db.onversionchange = () => db.close()
      if (settled) {
        db.close()
        return
      }
      settled = true
      resolve(db)
    }
    open.onerror = () => {
      if (settled) return
      settled = true
      reject(toArchiveStorageError(open.error, 'Could not open Image Studio archive database'))
    }
    open.onblocked = () => {
      if (settled) return
      settled = true
      reject(new Error('Image Studio archive database upgrade was blocked'))
    }
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
    transaction.onerror = () => reject(toArchiveStorageError(transaction.error, 'Image Studio archive transaction failed'))
    transaction.onabort = () => reject(toArchiveStorageError(transaction.error, 'Image Studio archive transaction was aborted'))
  })
}

function toArchiveStorageError(error: unknown, fallback: string): Error {
  if (isImageStudioArchiveLimitError(error)) {
    return error instanceof ImageStudioArchiveLimitError
      ? error
      : new ImageStudioArchiveLimitError(fallback)
  }
  return error instanceof Error ? error : new Error(fallback)
}

interface RasterDataURL {
  source: string
  mimeType: typeof safeDataMimes[number]
  payload: string
  bytes: number
}

function parseRasterDataURL(source: string): RasterDataURL | null {
  const comma = source.indexOf(',')
  if (comma < 0) return null
  const header = source.slice(5, comma).toLowerCase()
  const mimeType = safeDataMimes.find((mime) => header === `${mime};base64`)
  if (!mimeType) return null
  const payload = source.slice(comma + 1)
  if (!payload || payload.length % 4 === 1 || !/^[A-Za-z0-9+/]*={0,2}$/.test(payload)) return null
  if (payload.endsWith('=') && payload.length % 4 !== 0) return null
  return { source, mimeType, payload, bytes: base64PayloadBytes(payload) }
}

function base64PayloadBytes(payload: string): number {
  const padding = payload.endsWith('==') ? 2 : payload.endsWith('=') ? 1 : 0
  return Math.max(0, Math.floor(payload.length * 3 / 4) - padding)
}

function hasRasterSignature(parsed: RasterDataURL): boolean {
  let prefix: string
  try {
    prefix = atob(parsed.payload.slice(0, Math.min(parsed.payload.length, 16)))
  } catch {
    return false
  }
  const byte = (index: number) => prefix.charCodeAt(index)
  if (parsed.mimeType === 'image/png') {
    return prefix.length >= 8
      && byte(0) === 0x89 && prefix.slice(1, 4) === 'PNG'
      && byte(4) === 0x0d && byte(5) === 0x0a && byte(6) === 0x1a && byte(7) === 0x0a
  }
  if (parsed.mimeType === 'image/jpeg') {
    return prefix.length >= 3 && byte(0) === 0xff && byte(1) === 0xd8 && byte(2) === 0xff
  }
  return prefix.length >= 12 && prefix.slice(0, 4) === 'RIFF' && prefix.slice(8, 12) === 'WEBP'
}

async function sourceBlob(source: string): Promise<Blob> {
  if (source.startsWith('data:')) {
    const parsed = parseRasterDataURL(source)
    if (!parsed || parsed.bytes > thumbnailInputMaxBytes) throw new Error('Invalid image data')
    const binary = atob(parsed.payload)
    const bytes = new Uint8Array(binary.length)
    for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
    return new Blob([bytes], { type: parsed.mimeType })
  }

  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), 8_000)
  try {
    const response = await fetch(source, { mode: 'cors', credentials: 'omit', signal: controller.signal })
    if (!response.ok) throw new Error('Could not fetch image')
    const contentLength = Number(response.headers.get('content-length') || 0)
    if (contentLength > thumbnailInputMaxBytes) throw new Error('Image is too large to archive')
    if (!response.body) return await response.blob()
    const reader = response.body.getReader()
    const chunks: Uint8Array[] = []
    let bytes = 0
    try {
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        bytes += value.byteLength
        if (bytes > thumbnailInputMaxBytes) throw new Error('Image is too large to archive')
        chunks.push(value)
      }
    } catch (error) {
      await reader.cancel().catch(() => undefined)
      throw error
    }
    return new Blob(chunks, { type: response.headers.get('content-type')?.split(';')[0].trim().toLowerCase() || '' })
  } finally {
    window.clearTimeout(timeout)
  }
}

type ThumbnailImageSource = ImageBitmap | HTMLImageElement

async function loadThumbnailSource(blob: Blob): Promise<{
  image: ThumbnailImageSource
  width: number
  height: number
  close: () => void
}> {
  if (typeof createImageBitmap === 'function') {
    const bitmap = await createImageBitmap(blob)
    return { image: bitmap, width: bitmap.width, height: bitmap.height, close: () => bitmap.close() }
  }

  const url = URL.createObjectURL(blob)
  try {
    const image = await new Promise<HTMLImageElement>((resolve, reject) => {
      const element = new Image()
      element.onload = () => resolve(element)
      element.onerror = () => reject(new Error('Could not decode image'))
      element.src = url
    })
    return {
      image,
      width: image.naturalWidth || image.width,
      height: image.naturalHeight || image.height,
      close: () => URL.revokeObjectURL(url)
    }
  } catch (error) {
    URL.revokeObjectURL(url)
    throw error
  }
}

async function firstThumbnailCandidate(
  canvas: HTMLCanvasElement,
  mimeType: 'image/webp' | 'image/jpeg',
  qualities: number[]
): Promise<Blob | null> {
  for (const quality of qualities) {
    const blob = await canvasBlob(canvas, mimeType, quality)
    if (!blob || blob.size <= 0) continue
    const actualType = blob.type.toLowerCase()
    if (!safeDataMimes.includes(actualType as typeof safeDataMimes[number])) continue
    if (blob.size <= thumbnailMaxBytes) return blob
    if (actualType !== mimeType) break
  }
  return null
}

function canvasBlob(canvas: HTMLCanvasElement, mimeType: string, quality?: number): Promise<Blob | null> {
  return new Promise((resolve, reject) => {
    try {
      canvas.toBlob(resolve, mimeType, quality)
    } catch (error) {
      reject(error)
    }
  })
}

async function blobDataURL(blob: Blob): Promise<string> {
  if (blob.size <= 0 || blob.size > thumbnailMaxBytes) throw new Error('Invalid thumbnail size')
  const mimeType = blob.type.toLowerCase()
  if (!safeDataMimes.includes(mimeType as typeof safeDataMimes[number])) throw new Error('Invalid thumbnail type')
  const source = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => typeof reader.result === 'string'
      ? resolve(reader.result)
      : reject(new Error('Invalid thumbnail data'))
    reader.onerror = () => reject(reader.error || new Error('Could not read thumbnail'))
    reader.readAsDataURL(blob)
  })
  if (!normalizeThumbnailSource(source)) throw new Error('Invalid thumbnail data')
  return source
}

function boundedString(value: unknown, maxLength: number): string {
  if (typeof value !== 'string') return ''
  const normalized = value.trim()
  return normalized.length <= maxLength ? normalized : ''
}

function positiveInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : null
}

function positiveNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null
}

function nonNegativeInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null
}
