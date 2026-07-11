<template>
  <AppLayout>
    <div class="w-full space-y-6">
      <div v-if="gallery.length" class="flex justify-end">
        <button class="btn btn-secondary" type="button" @click="clearGallery">
          <Icon name="trash" size="sm" />
          <span>{{ t('imageStudio.clearGallery') }}</span>
        </button>
      </div>

      <section class="grid gap-6 lg:grid-cols-[minmax(0,0.9fr)_minmax(0,1.4fr)]">
        <form class="space-y-5 rounded-lg border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-900" @submit.prevent="generate">
          <div>
            <label class="input-label" for="image-studio-key">{{ t('imageStudio.apiKey') }}</label>
            <select id="image-studio-key" v-model.number="form.apiKeyId" class="input" :disabled="loadingKeys || generating">
              <option :value="0">{{ t('imageStudio.selectApiKey') }}</option>
              <option v-for="key in imageKeys" :key="key.id" :value="key.id">{{ key.name }}</option>
            </select>
          </div>

          <div v-if="usesGemini">
            <label class="input-label" for="image-studio-model">{{ t('imageStudio.model') }}</label>
            <select id="image-studio-model" v-model="form.model" class="input" :disabled="generating">
              <option v-for="model in geminiImageModels" :key="model" :value="model">{{ model }}</option>
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
            <label v-if="!usesGemini" class="block text-sm font-medium text-gray-700 dark:text-gray-200">
              {{ t('imageStudio.quality') }}
              <select v-model="form.quality" class="input mt-1.5" :disabled="generating">
                <option value="low">{{ t('imageStudio.qualityLow') }}</option>
                <option value="medium">{{ t('imageStudio.qualityMedium') }}</option>
                <option value="high">{{ t('imageStudio.qualityHigh') }}</option>
              </select>
            </label>
            <label v-if="!usesGemini" class="block text-sm font-medium text-gray-700 dark:text-gray-200">
              {{ t('imageStudio.background') }}
              <select id="image-studio-background" v-model="form.background" class="input mt-1.5" :disabled="generating">
                <option value="auto">{{ t('imageStudio.backgroundAuto') }}</option>
                <option value="opaque">{{ t('imageStudio.backgroundOpaque') }}</option>
              </select>
            </label>
            <label v-if="!usesGemini" class="block text-sm font-medium text-gray-700 dark:text-gray-200">
              {{ t('imageStudio.format') }}
              <select id="image-studio-format" v-model="form.outputFormat" class="input mt-1.5" :disabled="generating">
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
          <div v-else-if="gallery.length === 0 && generationPlaceholders.length === 0" class="grid min-h-80 place-items-center text-center text-sm text-gray-500 dark:text-gray-400">
            <div>
              <Icon name="grid" size="xl" class="mx-auto mb-3 opacity-50" />
              <p>{{ t('imageStudio.emptyGallery') }}</p>
            </div>
          </div>
          <div v-else class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3" aria-live="polite">
            <article
              v-for="placeholder in generationPlaceholders"
              :key="placeholder.id"
              class="overflow-hidden rounded-md border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800"
              data-testid="generation-placeholder"
            >
              <div class="image-studio-generation-canvas relative grid aspect-square place-items-center overflow-hidden bg-gray-100 dark:bg-dark-700">
                <template v-if="placeholder.status === 'generating'">
                  <span class="image-studio-scanline" aria-hidden="true" />
                  <div class="relative z-10 grid place-items-center text-gray-600 dark:text-gray-300">
                    <span class="image-studio-progress-ring mb-4" aria-hidden="true"><Icon name="grid" size="sm" /></span>
                    <p class="text-sm font-medium">{{ t('imageStudio.generatingImage') }}</p>
                  </div>
                </template>
                <div v-else class="relative z-10 grid max-w-48 place-items-center px-4 text-center">
                  <Icon name="x" size="lg" class="mb-3 text-red-500" />
                  <p class="text-sm font-medium text-gray-800 dark:text-gray-100">{{ t('imageStudio.generationFailed') }}</p>
                </div>
              </div>
              <div class="flex min-h-20 items-center justify-between gap-3 p-3">
                <template v-if="placeholder.status === 'generating'">
                  <p class="min-w-0 flex-1 truncate text-sm text-gray-600 dark:text-gray-300">{{ placeholder.prompt }}</p>
                  <span class="shrink-0 tabular-nums text-xs text-gray-500 dark:text-gray-400">{{ t('imageStudio.elapsedSeconds', { seconds: generationElapsedSeconds }) }}</span>
                </template>
                <template v-else>
                  <p class="min-w-0 flex-1 truncate text-sm text-red-600 dark:text-red-300">{{ placeholder.errorMessage }}</p>
                  <button class="btn btn-secondary shrink-0 px-2" type="button" :title="t('imageStudio.retryGeneration')" @click="retryGeneration">
                    <Icon name="refresh" size="sm" />
                    <span>{{ t('imageStudio.retryGeneration') }}</span>
                  </button>
                </template>
              </div>
            </article>

            <article
              v-for="item in gallery"
              :key="item.id"
              class="overflow-hidden rounded-md border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800"
              :class="{ 'image-studio-result-reveal': revealedItemIDs.has(item.id) }"
            >
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
    </div>

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
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
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
type GenerationPlaceholderStatus = 'generating' | 'error'
interface GenerationPlaceholder {
  id: string
  prompt: string
  status: GenerationPlaceholderStatus
  errorMessage: string
}
const generationPlaceholders = ref<GenerationPlaceholder[]>([])
const generationElapsedSeconds = ref(0)
const revealedItemIDs = ref(new Set<string>())
let controller: AbortController | null = null
let generationTimer: number | null = null
let generationSequence = 0

const form = reactive({
  apiKeyId: Number(localStorage.getItem('image_studio_api_key_id') || 0),
  model: localStorage.getItem('image_studio_gemini_model') || 'gemini-3.1-flash-image',
  prompt: '',
  size: '1024x1024',
  quality: 'medium' as ImageQuality,
  background: 'opaque' as ImageBackground,
  outputFormat: 'png' as ImageOutputFormat,
  count: 1
})

const canGenerate = computed(() => form.apiKeyId > 0 && form.prompt.trim().length > 0)
const geminiImageModels = [
  'gemini-3.1-flash-image',
  'gemini-3.1-flash-image-preview',
  'gemini-3.1-flash-lite-image',
  'gemini-3-pro-image',
  'gemini-3-pro-image-preview',
  'gemini-2.5-flash-image',
  'gemini-2.0-flash-exp-image-generation'
]
const selectedKey = computed(() => imageKeys.value.find((key) => key.id === form.apiKeyId))
const usesGemini = computed(() => ['gemini', 'antigravity'].includes(selectedKey.value?.group?.platform || ''))

function imageKeyAllowed(key: ApiKey): boolean {
  return key.status === 'active' &&
    ['openai', 'gemini', 'antigravity'].includes(key.group?.platform || '') &&
    key.group?.allow_image_generation === true
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
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('imageStudio.galleryLoadFailed'))
  } finally {
    loadingGallery.value = false
  }
}

function resultSource(result: ImagePlaygroundImage): string {
  if (result.b64_json) return `data:${result.mime_type || `image/${form.outputFormat}`};base64,${result.b64_json}`
  return result.url || ''
}

function resultFormat(result: ImagePlaygroundImage): ImageOutputFormat {
  const subtype = result.mime_type?.split('/')[1]?.toLowerCase()
  if (subtype === 'jpg' || subtype === 'jpeg') return 'jpeg'
  if (subtype === 'webp') return 'webp'
  if (subtype === 'png') return 'png'
  return form.outputFormat
}

function startGenerationPlaceholders() {
  stopGenerationTimer()
  generationElapsedSeconds.value = 0
  const prompt = form.prompt.trim()
  const count = Math.min(4, Math.max(1, Number(form.count) || 1))
  generationPlaceholders.value = Array.from({ length: count }, () => ({
    id: `generation-${Date.now()}-${generationSequence++}`,
    prompt,
    status: 'generating' as const,
    errorMessage: ''
  }))
  const startedAt = Date.now()
  generationTimer = window.setInterval(() => {
    generationElapsedSeconds.value = Math.floor((Date.now() - startedAt) / 1000)
  }, 1000)
}

function stopGenerationTimer() {
  if (generationTimer === null) return
  window.clearInterval(generationTimer)
  generationTimer = null
}

function clearGenerationPlaceholders() {
  generationPlaceholders.value = []
}

function failGenerationPlaceholders(message: string) {
  generationPlaceholders.value = generationPlaceholders.value.map((placeholder) => ({
    ...placeholder,
    status: 'error',
    errorMessage: message
  }))
}

async function generate() {
  if (!canGenerate.value || generating.value) return
  error.value = ''
  generating.value = true
  controller = new AbortController()
  startGenerationPlaceholders()
  try {
    const response = await generateImage({
      api_key_id: form.apiKeyId,
      ...(usesGemini.value ? { model: form.model } : {}),
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
      model: usesGemini.value ? form.model : 'gpt-image-2',
      size: form.size,
      quality: form.quality,
      background: form.background,
      outputFormat: resultFormat(result),
      imageSrc: resultSource(result)
    })).filter((item) => item.imageSrc)
    if (items.length === 0) throw new Error(t('imageStudio.noImagesReturned'))
    revealedItemIDs.value = new Set(items.map((item) => item.id))
    clearGenerationPlaceholders()
    gallery.value = [...items, ...gallery.value]
    appStore.showSuccess(t('imageStudio.generated', { count: items.length }))
    try {
      await Promise.all(items.map(saveImageStudioGalleryItem))
    } catch {
      appStore.showWarning(t('imageStudio.gallerySaveFailed'))
    }
  } catch (err: any) {
    const canceled = err?.code === 'ERR_CANCELED' || err?.name === 'AbortError'
    if (canceled) {
      clearGenerationPlaceholders()
    } else {
      error.value = extractApiErrorMessage(err, t('imageStudio.generateFailed'))
      failGenerationPlaceholders(error.value)
    }
  } finally {
    stopGenerationTimer()
    generating.value = false
    controller = null
  }
}

function cancelGeneration() {
  controller?.abort()
}

function retryGeneration() {
  if (!generating.value) void generate()
}

function reusePrompt(item: ImageStudioGalleryItem) {
  form.prompt = item.prompt
  form.apiKeyId = item.apiKeyId
  form.model = item.model || form.model
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
  const model = (item.model || 'image').replace(/[^a-zA-Z0-9._-]+/g, '-')
  return `sub2api-${model}-${item.createdAt}.${item.outputFormat || 'png'}`
}

watch(() => form.apiKeyId, (id) => localStorage.setItem('image_studio_api_key_id', String(id)))
watch(() => form.model, (model) => localStorage.setItem('image_studio_gemini_model', model))
onMounted(async () => { await Promise.all([loadKeys(), loadGallery()]) })
onUnmounted(() => {
  controller?.abort()
  stopGenerationTimer()
})
</script>

<style scoped>
.image-studio-progress-ring {
  align-items: center;
  border: 2px solid rgb(156 163 175 / 45%);
  border-top-color: rgb(37 99 235);
  border-radius: 9999px;
  display: inline-flex;
  height: 3.5rem;
  justify-content: center;
  width: 3.5rem;
  animation: image-studio-spin 1.4s linear infinite;
}

.image-studio-progress-ring :deep(*) {
  animation: image-studio-counter-spin 1.4s linear infinite;
}

.image-studio-scanline {
  background: rgb(37 99 235 / 55%);
  box-shadow: 0 0 12px rgb(37 99 235 / 35%);
  height: 2px;
  left: 10%;
  position: absolute;
  right: 10%;
  top: 12%;
  animation: image-studio-scan 2.4s ease-in-out infinite;
}

.image-studio-result-reveal {
  animation: image-studio-reveal 420ms ease-out both;
}

@keyframes image-studio-spin {
  to { transform: rotate(360deg); }
}

@keyframes image-studio-counter-spin {
  to { transform: rotate(-360deg); }
}

@keyframes image-studio-scan {
  0%, 100% { opacity: 0; top: 12%; }
  15%, 85% { opacity: 1; }
  50% { opacity: 0.75; top: 88%; }
}

@keyframes image-studio-reveal {
  from { opacity: 0.25; transform: scale(0.985); }
  to { opacity: 1; transform: scale(1); }
}

@media (prefers-reduced-motion: reduce) {
  .image-studio-progress-ring,
  .image-studio-progress-ring :deep(*),
  .image-studio-scanline,
  .image-studio-result-reveal {
    animation: none;
  }
}
</style>
