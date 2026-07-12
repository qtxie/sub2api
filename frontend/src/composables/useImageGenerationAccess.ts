import { computed, ref } from 'vue'
import { keysAPI } from '@/api/keys'
import { useAuthStore } from '@/stores/auth'
import type { ApiKey } from '@/types'

const loaded = ref(false)
const loading = ref(false)
const hasAllowedImageKey = ref(false)
let pendingLoad: Promise<boolean> | null = null
const pageSize = 100
const imageGenerationPlatforms = new Set(['openai', 'gemini', 'antigravity'])

function keyAllowsImageGeneration(key: ApiKey): boolean {
  return key.status === 'active' &&
    imageGenerationPlatforms.has(key.group?.platform || '') &&
    key.group?.allow_image_generation === true
}

async function loadImageGenerationAccess(force = false): Promise<boolean> {
  const authStore = useAuthStore()
  if (!authStore.isAuthenticated) {
    loaded.value = true
    hasAllowedImageKey.value = false
    return false
  }
  if (loaded.value && !force) return hasAllowedImageKey.value
  if (pendingLoad && !force) return pendingLoad

  loading.value = true
  pendingLoad = (async () => {
    let page = 1
    while (true) {
      const result = await keysAPI.list(page, pageSize, { status: 'active' })
      if ((result.items || []).some(keyAllowsImageGeneration)) {
        hasAllowedImageKey.value = true
        loaded.value = true
        return true
      }
      if (page >= result.pages || (result.items || []).length === 0) {
        hasAllowedImageKey.value = false
        loaded.value = true
        return false
      }
      page += 1
    }
  })().catch(() => {
    hasAllowedImageKey.value = false
    loaded.value = true
    return false
  }).finally(() => {
    loading.value = false
    pendingLoad = null
  })
  return pendingLoad
}

export function useImageGenerationAccess() {
  return {
    canUseImageGeneration: computed(() => hasAllowedImageKey.value),
    imageGenerationAccessLoaded: computed(() => loaded.value),
    imageGenerationAccessLoading: computed(() => loading.value),
    refreshImageGenerationAccess: loadImageGenerationAccess
  }
}
