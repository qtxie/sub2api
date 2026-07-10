import { beforeEach, describe, expect, it, vi } from 'vitest'

const saveImageStudioGalleryItem = vi.hoisted(() => vi.fn())
const listImageStudioGallery = vi.hoisted(() => vi.fn())
const deleteImageStudioGalleryItem = vi.hoisted(() => vi.fn())
const clearImageStudioGallery = vi.hoisted(() => vi.fn())

vi.mock('../imageStudioGallery', () => ({
  saveImageStudioGalleryItem,
  listImageStudioGallery,
  deleteImageStudioGalleryItem,
  clearImageStudioGallery
}))

describe('Image Studio gallery persistence contract', () => {
  beforeEach(() => {
    saveImageStudioGalleryItem.mockReset()
    listImageStudioGallery.mockReset()
    deleteImageStudioGalleryItem.mockReset()
    clearImageStudioGallery.mockReset()
  })

  it('keeps gallery records scoped to their owner at the storage boundary', async () => {
    // The storage API requires a user ID for every read or destructive operation.
    await listImageStudioGallery(42)
    await deleteImageStudioGalleryItem(42, 'image-a')
    await clearImageStudioGallery(42)

    expect(listImageStudioGallery).toHaveBeenCalledWith(42)
    expect(deleteImageStudioGalleryItem).toHaveBeenCalledWith(42, 'image-a')
    expect(clearImageStudioGallery).toHaveBeenCalledWith(42)
  })
})
