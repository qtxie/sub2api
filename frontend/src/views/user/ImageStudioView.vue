<template>
  <AppLayout>
    <main class="mx-auto w-full max-w-7xl space-y-6 px-4 py-6 sm:px-6">
      <header class="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('imageStudio.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('imageStudio.description') }}</p>
        </div>
        <button v-if="gallery.length" class="btn btn-secondary" type="button" @click="clearGallery">
          <Icon name="trash" size="sm" />
          <span>{{ t('imageStudio.clearGallery') }}</span>
        </button>
      </header>

      <section class="grid gap-6 lg:grid-cols-[minmax(0,0.9fr)_minmax(0,1.4fr)]">
        <form class="space-y-5 rounded-lg border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-900" @submit.prevent="generate">
          <div>
            <label class="input-label" for="image-studio-key">{{ t('imageStudio.apiKey') }}</label>
            <select id="image-studio-key" v-model.number="form.apiKeyId" class="input" :disabled="loadingKeys || generating">
              <option :value="0">{{ t('imageStudio.selectApiKey') }}</option>
              <option v-for="key in imageKeys" :key="key.id" :value="key.id">{{ key.name }}</option>
            </select>
          </div>

          <div>
            <label class="input-label" for="image-studio-prompt">{{ t('imageStudio.prompt') }}</label>
            <textarea id="image-studio-prompt" v-model="form.prompt" class="input min-h-36 resize-y" :placeholder="t('imageStudio.promptPlaceholder')" :disabled="generating" />
          </div>

          <div class="grid grid-cols-2 gap-3">
            <label class="block text-sm font-medium text-gray-700 dark:text-gray-200">
              {{ t('imageStudio.size') }}
              <select v-model="form.size" class="input mt-1.5" :disabled="generating">
                <option value="1024x1024">1024 x 1024</option>
                <option value="1536x1024">1536 x 1024</option>
                <option value="1024x1536">1024 x 1536</option>
              </select>
            </label>
            <label class="block text-sm font-medium text-gray-700 dark:text-gray-200">
              {{ t('imageStudio.quality') }}
              <select v-model="form.quality" class="input mt-1.5" :disabled="generating">
                <option value="low">{{ t('imageStudio.qualityLow') }}</option>
                <option value="medium">{{ t('imageStudio.qualityMedium') }}</option>
                <option value="high">{{ t('imageStudio.qualityHigh') }}</option>
              </select>
            </label>
            <label class="block text-sm font-medium text-gray-700 dark:text-gray-200">
              {{ t('imageStudio.background') }}
              <select v-model="form.background" class="input mt-1.5" :disabled="generating">
                <option value="opaque">{{ t('imageStudio.backgroundOpaque') }}</option>
                <option value="transparent">{{ t('imageStudio.backgroundTransparent') }}</option>
              </select>
            </label>
            <label class="block text-sm font-medium text-gray-700 dark:text-gray-200">
              {{ t('imageStudio.format') }}
              <select v-model="form.outputFormat" class="input mt-1.5" :disabled="generating">
                <option value="png">PNG</option>
                <option value="jpeg">JPEG</option>
                <option value="webp">WebP</option>
              </select>
            </label>
          </div>

          <label class="block text-sm font-medium text-gray-700 dark:text-gray-200">
            {{ t('imageStudio.count') }}
            <input v-model.number="form.count" class="input mt-1.5" type="number" min="1" max="4" :disabled="generating" />
          </label>

          <p v-if="!loadingKeys && imageKeys.length === 0" class="rounded-md bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">
            {{ t('imageStudio.noEligibleKeys') }}
          </p>
          <p v-if="error" class="rounded-md bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-200">{{ error }}</p>

          <div class="flex gap-3">
            <button class="btn btn-primary flex-1" type="submit" :disabled="generating || !canGenerate">
              <Icon name="grid" size="sm" :class="{ 'animate-pulse': generating }" />
              <span>{{ generating ? t('imageStudio.generating') : t('imageStudio.generate') }}</span>
            </button>
            <button v-if="generating" class="btn btn-secondary" type="button" @click="cancelGeneration">{{ t('common.cancel') }}</button>
          </div>
        </form>

        <section class="min-h-96 rounded-lg border border-gray-200 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-900/50">
          <div v-if="loadingGallery" class="grid min-h-80 place-items-center text-sm text-gray-500">{{ t('common.loading') }}</div>
          <div v-else-if="gallery.length === 0" class="grid min-h-80 place-items-center text-center text-sm text-gray-500 dark:text-gray-400">
            <div>
              <Icon name="grid" size="xl" class="mx-auto mb-3 opacity-50" />
              <p>{{ t('imageStudio.emptyGallery') }}</p>
            </div>
          </div>
          <div v-else class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <article v-for="item in gallery" :key="item.id" class="overflow-hidden rounded-md border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
              <button class="block aspect-square w-full bg-gray-100 dark:bg-dark-700" type="button" @click="previewItem = item">
                <img :src="item.imageSrc" :alt="item.prompt" class="h-full w-full object-cover" />
              </button>
              <div class="space-y-3 p-3">
                <p class="line-clamp-2 text-sm text-gray-700 dark:text-gray-200">{{ item.revisedPrompt || item.prompt }}</p>
                <div class="flex items-center justify-between gap-2">
                  <button class="btn btn-secondary px-2" type="button" :title="t('imageStudio.reusePrompt')" @click="reusePrompt(item)"><Icon name="refresh" size="sm" /></button>
                  <a class="btn btn-secondary px-2" :href="item.imageSrc" :download="downloadName(item)" :title="t('imageStudio.download')"><Icon name="download" size="sm" /></a>
                  <button class="btn btn-secondary px-2 text-red-600" type="button" :title="t('common.delete')" @click="removeItem(item.id)"><Icon name="trash" size="sm" /></button>
                </div>
              </div>
            </article>
          </div>
        </section>
      </section>
    </main>

    <div v-if="previewItem" class="fixed inset-0 z-50 grid place-items-center bg-black/75 p-4" @click.self="previewItem = null">
      <section class="max-h-full max-w-5xl overflow-auto rounded-lg bg-white p-4 shadow-xl dark:bg-dark-900">
        <div class="mb-3 flex justify-end"><button class="btn btn-secondary px-2" type="button" @click="previewItem = null"><Icon name="x" size="sm" /></button></div>
        <img :src="previewItem.imageSrc" :alt="previewItem.prompt" class="max-h-[72vh] max-w-full object-contain" />
        <p class="mt-3 max-w-3xl text-sm text-gray-600 dark:text-gray-300">{{ previewItem.revisedPrompt || previewItem.prompt }}</p>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { keysAPI } from '@/api/keys'
import { generateImage, type ImageBackground, type ImageOutputFormat, type ImagePlaygroundImage, type ImageQuality } from '@/api/imagePlayground'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { extractApiErrorMessage } from '@/utils/apiError'
import { clearImageStudioGallery, deleteImageStudioGalleryItem, listImageStudioGallery, saveImageStudioGalleryItem, type ImageStudioGalleryItem } from '@/utils/imageStudioGallery'
import type { ApiKey } from '@/types'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
const imageKeys = ref<ApiKey[]>([])
const gallery = ref<ImageStudioGalleryItem[]>([])
const loadingKeys = ref(true)
const loadingGallery = ref(true)
const generating = ref(false)
const error = ref('')
const previewItem = ref<ImageStudioGalleryItem | null>(null)
let controller: AbortController | null = null

const form = reactive({
  apiKeyId: Number(localStorage.getItem('image_studio_api_key_id') || 0),
  prompt: '',
  size: '1024x1024',
  quality: 'medium' as ImageQuality,
  background: 'opaque' as ImageBackground,
  outputFormat: 'png' as ImageOutputFormat,
  count: 1
})

const canGenerate = computed(() => form.apiKeyId > 0 && form.prompt.trim().length > 0)

function imageKeyAllowed(key: ApiKey): boolean {
  return key.status === 'active' &&
    key.group?.platform === 'openai' &&
    key.group.allow_image_generation === true
}

async function loadKeys() {
  loadingKeys.value = true
  try {
    const keys: ApiKey[] = []
    let page = 1
    while (true) {
      const result = await keysAPI.list(page, 100, { status: 'active' })
      keys.push(...(result.items || []).filter(imageKeyAllowed))
      if (page >= result.pages || (result.items || []).length === 0) break
      page += 1
    }
    imageKeys.value = keys
    if (!imageKeys.value.some((key) => key.id === form.apiKeyId)) form.apiKeyId = imageKeys.value[0]?.id || 0
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('imageStudio.loadKeysFailed'))
  } finally {
    loadingKeys.value = false
  }
}

async function loadGallery() {
  loadingGallery.value = true
  try {
    gallery.value = await listImageStudioGallery(authStore.user?.id || 0)
  } finally {
    loadingGallery.value = false
  }
}

function resultSource(result: ImagePlaygroundImage): string {
  if (result.b64_json) return `data:image/${form.outputFormat};base64,${result.b64_json}`
  return result.url || ''
}

async function generate() {
  if (!canGenerate.value || generating.value) return
  error.value = ''
  generating.value = true
  controller = new AbortController()
  try {
    const response = await generateImage({
      api_key_id: form.apiKeyId,
      prompt: form.prompt.trim(),
      size: form.size,
      quality: form.quality,
      background: form.background,
      output_format: form.outputFormat,
      n: form.count
    }, controller.signal)
    const createdAt = Date.now()
    const items = (response.data || []).map((result, index) => ({
      id: crypto.randomUUID ? crypto.randomUUID() : `${createdAt}-${index}-${Math.random()}`,
      userId: authStore.user?.id || 0,
      createdAt,
      prompt: form.prompt.trim(),
      revisedPrompt: result.revised_prompt,
      apiKeyId: form.apiKeyId,
      size: form.size,
      quality: form.quality,
      background: form.background,
      outputFormat: form.outputFormat,
      imageSrc: resultSource(result)
    })).filter((item) => item.imageSrc)
    gallery.value = [...items, ...gallery.value]
    appStore.showSuccess(t('imageStudio.generated', { count: items.length }))
    try {
      await Promise.all(items.map(saveImageStudioGalleryItem))
    } catch {
      appStore.showWarning(t('imageStudio.gallerySaveFailed'))
    }
  } catch (err: any) {
    if (err?.code !== 'ERR_CANCELED') error.value = extractApiErrorMessage(err, t('imageStudio.generateFailed'))
  } finally {
    generating.value = false
    controller = null
  }
}

function cancelGeneration() {
  controller?.abort()
}

function reusePrompt(item: ImageStudioGalleryItem) {
  form.prompt = item.prompt
  form.apiKeyId = item.apiKeyId
  form.size = item.size || form.size
  form.quality = (item.quality as ImageQuality) || form.quality
  form.background = (item.background as ImageBackground) || form.background
  form.outputFormat = (item.outputFormat as ImageOutputFormat) || form.outputFormat
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

async function removeItem(id: string) {
  await deleteImageStudioGalleryItem(authStore.user?.id || 0, id)
  gallery.value = gallery.value.filter((item) => item.id !== id)
  if (previewItem.value?.id === id) previewItem.value = null
}

async function clearGallery() {
  await clearImageStudioGallery(authStore.user?.id || 0)
  gallery.value = []
  previewItem.value = null
}

function downloadName(item: ImageStudioGalleryItem): string {
  return `sub2api-gpt-image-2-${item.createdAt}.${item.outputFormat || 'png'}`
}

watch(() => form.apiKeyId, (id) => localStorage.setItem('image_studio_api_key_id', String(id)))
onMounted(async () => { await Promise.all([loadKeys(), loadGallery()]) })
</script>
