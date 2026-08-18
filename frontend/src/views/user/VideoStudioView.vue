<template>
  <AppLayout>
    <div class="video-studio-shell">
      <div class="studio-status" aria-live="polite">
        <div class="studio-model-mark">
          <Icon name="play" size="sm" />
          <span>{{ capabilities?.label || t('videoStudio.model') }}</span>
        </div>
        <span class="studio-status-divider"></span>
        <span class="studio-status-key">{{ selectedKey?.name || t('videoStudio.selectApiKey') }}</span>
        <div class="studio-price-summary" data-testid="video-pricing">
          <span>{{ t('videoStudio.estimate') }}</span>
          <strong>{{ formattedEstimate }}</strong>
        </div>
      </div>

      <form class="studio-grid" @submit.prevent="generate">
        <aside class="studio-controls" :aria-label="t('videoStudio.controls')">
          <div class="studio-control-scroll">
            <div class="control-group">
              <label for="video-studio-key" class="control-label">{{ t('videoStudio.apiKey') }}</label>
              <select
                id="video-studio-key"
                v-model.number="form.apiKeyId"
                class="studio-select"
                :disabled="loadingKeys || grokKeys.length === 0 || submitting"
              >
                <option :value="0" disabled>{{ t('videoStudio.selectApiKey') }}</option>
                <option v-for="key in grokKeys" :key="key.id" :value="key.id">{{ key.name }}</option>
              </select>
            </div>

            <p v-if="capabilitiesError" class="control-error" role="alert">{{ capabilitiesError }}</p>

            <div class="control-group duration-control">
              <span class="control-label">{{ t('videoStudio.duration') }}</span>
              <div class="stepper">
                <button
                  type="button"
                  :title="t('videoStudio.decreaseDuration')"
                  :disabled="submitting || form.duration <= minDuration"
                  @click="form.duration--"
                >-</button>
                <output>{{ t('videoStudio.seconds', { count: form.duration }) }}</output>
                <button
                  type="button"
                  :title="t('videoStudio.increaseDuration')"
                  :disabled="submitting || form.duration >= maxDuration"
                  @click="form.duration++"
                >+</button>
              </div>
            </div>

            <div class="control-group">
              <label for="video-studio-aspect-ratio" class="control-label">{{ t('videoStudio.aspectRatio') }}</label>
              <div class="ratio-select-shell">
                <span class="ratio-shape" :class="ratioShape(form.aspectRatio)" aria-hidden="true"></span>
                <select
                  id="video-studio-aspect-ratio"
                  v-model="form.aspectRatio"
                  class="studio-select ratio-select"
                  :disabled="submitting || loadingCapabilities"
                >
                  <option v-for="ratio in aspectRatios" :key="ratio" :value="ratio">{{ ratio }}</option>
                </select>
              </div>
            </div>

            <fieldset class="control-group">
              <legend class="control-label">{{ t('videoStudio.resolution') }}</legend>
              <div class="segmented-control three-columns">
                <button
                  v-for="resolution in resolutions"
                  :key="resolution"
                  type="button"
                  :class="{ active: form.resolution === resolution }"
                  :aria-pressed="form.resolution === resolution"
                  :disabled="submitting"
                  @click="form.resolution = resolution"
                >{{ resolution.toUpperCase() }}</button>
              </div>
            </fieldset>
          </div>

          <div class="studio-price-area">
            <div class="price-detail">
              <span v-if="loadingPricing">{{ t('common.loading') }}</span>
              <span v-else-if="selectedUnitPrice != null">
                {{ t('videoStudio.perSecond', { price: formatPrice(selectedUnitPrice) }) }}
              </span>
              <span v-else-if="pricing?.pricing_kind === 'usage_based'">{{ t('videoStudio.usageBasedPricing') }}</span>
              <span v-else>{{ pricingError || t('videoStudio.pricingUnavailable') }}</span>
            </div>
            <strong>{{ formattedEstimate }}</strong>
          </div>
        </aside>

        <div class="studio-workspace">
          <section class="prompt-workspace" aria-labelledby="video-studio-prompt-label">
            <div class="prompt-header">
              <label id="video-studio-prompt-label" for="video-studio-prompt" class="control-label">
                {{ t('videoStudio.prompt') }}
              </label>
              <span>{{ form.prompt.length }}</span>
            </div>
            <textarea
              id="video-studio-prompt"
              v-model="form.prompt"
              class="studio-prompt"
              :placeholder="t('videoStudio.promptPlaceholder')"
              rows="10"
              :disabled="submitting"
            ></textarea>
            <div class="prompt-submit-area">
              <span class="prompt-selection">
                {{ form.aspectRatio }} · {{ form.resolution.toUpperCase() }} · {{ t('videoStudio.seconds', { count: form.duration }) }}
              </span>
              <button type="submit" class="generate-button" :disabled="!canGenerate">
                <span v-if="submitting" class="loading-ring small"></span>
                <Icon v-else name="play" size="sm" />
                <span>{{ submitting ? t('videoStudio.submitting') : t('videoStudio.generate') }}</span>
              </button>
            </div>
          </section>

          <section class="studio-gallery" aria-labelledby="video-studio-gallery-title">
            <header class="gallery-header">
              <div>
                <h2 id="video-studio-gallery-title">{{ t('videoStudio.gallery') }}</h2>
                <p>{{ t('videoStudio.galleryCount', { count: gallery.length }) }}</p>
              </div>
              <button
                v-if="gallery.length > 0"
                type="button"
                class="toolbar-icon-button"
                :title="t('videoStudio.clearGallery')"
                @click="clearGallery"
              >
                <Icon name="trash" size="sm" />
              </button>
            </header>

            <div v-if="loadingGallery" class="gallery-empty">
              <span class="loading-ring"></span>
              <p>{{ t('common.loading') }}</p>
            </div>

            <div v-else-if="!loadingKeys && grokKeys.length === 0 && gallery.length === 0" class="gallery-empty">
              <span class="empty-icon"><Icon name="key" size="lg" /></span>
              <h3>{{ t('videoStudio.noKeysTitle') }}</h3>
              <p>{{ t('videoStudio.noEligibleKeys') }}</p>
              <router-link to="/keys" class="btn btn-primary">
                <Icon name="key" size="sm" class="mr-2" />
                {{ t('videoStudio.manageKeys') }}
              </router-link>
            </div>

            <div v-else-if="gallery.length === 0" class="gallery-empty">
              <span class="empty-icon"><Icon name="play" size="lg" /></span>
              <h3>{{ t('videoStudio.emptyTitle') }}</h3>
              <p>{{ t('videoStudio.emptyGallery') }}</p>
            </div>

            <div v-else class="gallery-grid">
              <article v-for="item in gallery" :key="item.id" class="video-item">
                <div class="video-frame" :style="aspectRatioStyle(item.aspectRatio)">
                  <video
                    v-if="videoURLs[item.id]"
                    :src="videoURLs[item.id]"
                    controls
                    preload="metadata"
                    :aria-label="item.prompt"
                  ></video>
                  <div v-else class="video-state" :class="`state-${displayStatus(item)}`">
                    <template v-if="item.status === 'pending' && !item.trackingPaused">
                      <div
                        v-if="typeof item.progress === 'number'"
                        class="video-progress"
                        role="progressbar"
                        :aria-valuenow="Math.round(item.progress)"
                        aria-valuemin="0"
                        aria-valuemax="100"
                      >
                        <span class="video-progress-track"><span :style="{ width: `${item.progress}%` }"></span></span>
                        <strong>{{ Math.round(item.progress) }}%</strong>
                      </div>
                      <span v-else class="loading-ring"></span>
                      <strong>{{ t('videoStudio.pending') }}</strong>
                      <small>{{ t('videoStudio.elapsed', { seconds: elapsedSeconds(item) }) }}</small>
                    </template>
                    <template v-else-if="item.status === 'pending'">
                      <Icon name="clock" size="lg" />
                      <strong>{{ t('videoStudio.trackingPaused') }}</strong>
                    </template>
                    <template v-else-if="item.status === 'done' && contentLoadingIds.has(item.id)">
                      <span class="loading-ring"></span>
                      <strong>{{ t('videoStudio.loadingVideo') }}</strong>
                    </template>
                    <template v-else>
                      <Icon :name="item.status === 'expired' ? 'clock' : 'exclamationCircle'" size="lg" />
                      <strong>{{ t(`videoStudio.${item.status}`) }}</strong>
                      <small v-if="item.lastError">{{ item.lastError }}</small>
                      <button
                        v-if="item.status === 'done'"
                        type="button"
                        class="state-action"
                        @click="loadCompletedVideo(item)"
                      >{{ t('videoStudio.retryContent') }}</button>
                    </template>
                  </div>
                </div>

                <div class="video-item-meta">
                  <div class="video-copy">
                    <p>{{ item.prompt }}</p>
                    <span>{{ formatCreatedAt(item.createdAt) }} · {{ item.aspectRatio }} · {{ item.resolution.toUpperCase() }} · {{ t('videoStudio.seconds', { count: item.completedDuration || item.duration }) }}</span>
                  </div>
                  <div class="video-actions">
                    <button
                      v-if="item.status === 'pending' && !item.trackingPaused"
                      type="button"
                      :title="t('videoStudio.stopTracking')"
                      @click="stopTracking(item)"
                    >
                      <Icon name="x" size="sm" />
                    </button>
                    <button
                      v-else-if="item.status === 'pending'"
                      type="button"
                      :title="t('videoStudio.resumeTracking')"
                      :disabled="!keyAvailable(item.apiKeyId)"
                      @click="resumeTracking(item)"
                    >
                      <Icon name="refresh" size="sm" />
                    </button>
                    <a
                      v-if="videoURLs[item.id]"
                      :href="videoURLs[item.id]"
                      :download="downloadName(item)"
                      :title="t('videoStudio.download')"
                    >
                      <Icon name="download" size="sm" />
                    </a>
                    <button type="button" :title="t('videoStudio.delete')" @click="deleteItem(item)">
                      <Icon name="trash" size="sm" />
                    </button>
                  </div>
                </div>
              </article>
            </div>
          </section>
        </div>
      </form>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { Icon } from '@/components/icons'
import { keysAPI } from '@/api/keys'
import {
  generateVideo,
  getVideoContent,
  getVideoStatus,
  getVideoStudioCapabilities,
  getVideoStudioPricing,
  VIDEO_STUDIO_ASPECT_RATIOS,
  VIDEO_STUDIO_DURATIONS,
  VIDEO_STUDIO_MODEL,
  VIDEO_STUDIO_RESOLUTIONS,
  type VideoStudioAspectRatio,
  type VideoStudioCapabilities,
  type VideoStudioPricingResponse,
  type VideoStudioResolution,
  type VideoStudioStatusResponse
} from '@/api/videoStudio'
import type { ApiKey } from '@/types'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useVideoStudioPolling, type VideoStudioPollingTarget } from '@/composables/useVideoStudioPolling'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  clearVideoStudioGallery,
  deleteVideoStudioGalleryItem,
  expireStaleVideoStudioTasks,
  listVideoStudioGallery,
  saveVideoStudioGalleryItem,
  VIDEO_STUDIO_TASK_BINDING_MS,
  videoStudioTaskID,
  type VideoStudioGalleryItem
} from '@/utils/videoStudioGallery'

const { t, locale } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
const userId = computed(() => authStore.user?.id || 0)

const grokKeys = ref<ApiKey[]>([])
const gallery = ref<VideoStudioGalleryItem[]>([])
const capabilities = ref<VideoStudioCapabilities | null>(null)
const pricing = ref<VideoStudioPricingResponse | null>(null)
const loadingKeys = ref(true)
const loadingGallery = ref(true)
const loadingCapabilities = ref(false)
const loadingPricing = ref(false)
const submitting = ref(false)
const capabilitiesError = ref('')
const pricingError = ref('')
const now = ref(Date.now())
const videoURLs = reactive<Record<string, string>>({})
const contentLoadingIds = ref(new Set<string>())
const contentControllers = new Map<string, AbortController>()
let capabilitiesController: AbortController | null = null
let pricingController: AbortController | null = null
let generationController: AbortController | null = null
let elapsedTimer: ReturnType<typeof setInterval> | null = null

const form = reactive({
  apiKeyId: 0,
  prompt: '',
  duration: VIDEO_STUDIO_DURATIONS.default as number,
  aspectRatio: '16:9' as VideoStudioAspectRatio,
  resolution: '480p' as VideoStudioResolution
})

const selectedKey = computed(() => grokKeys.value.find((key) => key.id === form.apiKeyId) || null)
const minDuration = computed(() => capabilities.value?.min_duration || VIDEO_STUDIO_DURATIONS.min)
const maxDuration = computed(() => capabilities.value?.max_duration || VIDEO_STUDIO_DURATIONS.max)
const aspectRatios = computed(() => capabilities.value?.aspect_ratios || [...VIDEO_STUDIO_ASPECT_RATIOS])
const resolutions = computed(() => capabilities.value?.resolutions || [...VIDEO_STUDIO_RESOLUTIONS])
const selectedPrice = computed(() => pricing.value?.prices.find((price) => price.resolution === form.resolution))
const selectedUnitPrice = computed(() => selectedPrice.value?.unit_price ?? null)
const selectedTotalPrice = computed(() => selectedPrice.value?.total_price
  ?? (selectedUnitPrice.value === null ? null : selectedUnitPrice.value * form.duration))
const formattedEstimate = computed(() => {
  if (loadingPricing.value) return '...'
  if (selectedTotalPrice.value !== null) return formatPrice(selectedTotalPrice.value)
  if (pricing.value?.pricing_kind === 'usage_based') return t('videoStudio.usageBasedPricing')
  return '—'
})
const canGenerate = computed(() => {
  return !submitting.value
    && !loadingCapabilities.value
    && Boolean(capabilities.value)
    && form.apiKeyId > 0
    && form.prompt.trim().length > 0
    && Number.isInteger(form.duration)
    && form.duration >= minDuration.value
    && form.duration <= maxDuration.value
    && aspectRatios.value.includes(form.aspectRatio)
    && resolutions.value.includes(form.resolution)
})

const poller = useVideoStudioPolling({
  poll: (target, signal) => getVideoStatus(target.apiKeyId, target.requestId, signal),
  onResult: handlePollResult,
  onError: handlePollError
})

function storageKey(): string {
  return `video_studio_settings:${userId.value}`
}

function keyAllowed(key: ApiKey): boolean {
  return key.status === 'active'
    && key.group?.platform === 'grok'
    && key.group.allow_image_generation === true
}

async function loadKeys() {
  loadingKeys.value = true
  try {
    const keys: ApiKey[] = []
    let page = 1
    while (true) {
      const result = await keysAPI.list(page, 100, { status: 'active' })
      keys.push(...(result.items || []).filter(keyAllowed))
      if (page >= result.pages || (result.items || []).length === 0) break
      page += 1
    }
    grokKeys.value = keys
    restoreSettings()
    if (!keys.some((key) => key.id === form.apiKeyId)) form.apiKeyId = keys[0]?.id || 0
    await pauseTasksWithoutKeys()
    if (form.apiKeyId) await loadCapabilities()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('videoStudio.loadKeysFailed')))
  } finally {
    loadingKeys.value = false
  }
}

async function loadCapabilities() {
  capabilitiesController?.abort()
  capabilities.value = null
  capabilitiesError.value = ''
  pricing.value = null
  pricingError.value = ''
  if (!form.apiKeyId) return
  const requestedKeyId = form.apiKeyId
  const controller = new AbortController()
  capabilitiesController = controller
  loadingCapabilities.value = true
  try {
    const result = await getVideoStudioCapabilities(requestedKeyId, controller.signal)
    if (capabilitiesController !== controller || form.apiKeyId !== requestedKeyId) return
    capabilities.value = result
    form.duration = clampDuration(form.duration)
    if (!result.aspect_ratios.includes(form.aspectRatio)) form.aspectRatio = result.aspect_ratios[0]
    if (!result.resolutions.includes(form.resolution)) form.resolution = result.resolutions[0]
    await loadPricing()
  } catch (error: any) {
    if (capabilitiesController === controller && !isCanceled(error)) {
      capabilitiesError.value = extractApiErrorMessage(error, t('videoStudio.loadCapabilitiesFailed'))
    }
  } finally {
    if (capabilitiesController === controller) {
      capabilitiesController = null
      loadingCapabilities.value = false
    }
  }
}

async function loadPricing() {
  pricingController?.abort()
  pricing.value = null
  pricingError.value = ''
  if (!form.apiKeyId || !capabilities.value) return
  const requestedKeyId = form.apiKeyId
  const requestedDuration = form.duration
  const controller = new AbortController()
  pricingController = controller
  loadingPricing.value = true
  try {
    const result = await getVideoStudioPricing(requestedKeyId, requestedDuration, controller.signal)
    if (pricingController === controller && form.apiKeyId === requestedKeyId && form.duration === requestedDuration) {
      pricing.value = result
    }
  } catch (error: any) {
    if (pricingController === controller && !isCanceled(error)) {
      pricingError.value = extractApiErrorMessage(error, t('videoStudio.loadPricingFailed'))
    }
  } finally {
    if (pricingController === controller) {
      pricingController = null
      loadingPricing.value = false
    }
  }
}

async function loadGallery() {
  loadingGallery.value = true
  try {
    const restored = expireStaleVideoStudioTasks(await listVideoStudioGallery(userId.value))
    gallery.value = restored.items
    await Promise.allSettled(restored.expired.map((item) => saveVideoStudioGalleryItem(item)))
    for (const item of gallery.value) {
      if (item.videoBlob) createVideoURL(item)
    }
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('videoStudio.galleryLoadFailed')))
  } finally {
    loadingGallery.value = false
  }
}

function resumePersistedTasks() {
  for (const item of gallery.value) {
    if (item.status === 'pending' && !item.trackingPaused && keyAvailable(item.apiKeyId)) {
      poller.track({ requestId: item.requestId, apiKeyId: item.apiKeyId }, true)
    }
  }
}

async function pauseTasksWithoutKeys() {
  const updates: Promise<unknown>[] = []
  gallery.value = gallery.value.map((item) => {
    if (item.status !== 'pending' || item.trackingPaused || keyAvailable(item.apiKeyId)) return item
    const next = { ...item, trackingPaused: true, updatedAt: Date.now() }
    updates.push(saveVideoStudioGalleryItem(next))
    return next
  })
  await Promise.allSettled(updates)
}

async function generate() {
  if (!canGenerate.value) return
  const prompt = form.prompt.trim()
  submitting.value = true
  generationController = new AbortController()
  try {
    const result = await generateVideo({
      api_key_id: form.apiKeyId,
      prompt,
      duration: form.duration,
      aspect_ratio: form.aspectRatio,
      resolution: form.resolution
    }, generationController.signal)
    const createdAt = Date.now()
    const item: VideoStudioGalleryItem = {
      id: videoStudioTaskID(userId.value, result.request_id),
      requestId: result.request_id,
      userId: userId.value,
      apiKeyId: form.apiKeyId,
      createdAt,
      updatedAt: createdAt,
      prompt,
      model: VIDEO_STUDIO_MODEL,
      duration: form.duration,
      aspectRatio: form.aspectRatio,
      resolution: form.resolution,
      status: result.status,
      trackingPaused: false
    }
    gallery.value = [item, ...gallery.value.filter((entry) => entry.id !== item.id)]
    await persistTask(item, true)
    appStore.showSuccess(t('videoStudio.generated'))
    if (item.status === 'pending') {
      poller.track({ requestId: item.requestId, apiKeyId: item.apiKeyId })
    } else if (item.status === 'done') {
      await loadCompletedVideo(item)
    }
  } catch (error: any) {
    if (!isCanceled(error)) appStore.showError(extractApiErrorMessage(error, t('videoStudio.generationFailed')))
  } finally {
    submitting.value = false
    generationController = null
  }
}

async function handlePollResult(target: VideoStudioPollingTarget, result: VideoStudioStatusResponse) {
  const item = gallery.value.find((entry) => entry.requestId === target.requestId)
  if (!item) return

  if (result.status === 'pending' && Date.now() - item.createdAt >= VIDEO_STUDIO_TASK_BINDING_MS) {
    await expirePendingTask(item)
    return
  }

  const next: VideoStudioGalleryItem = {
    ...item,
    status: result.status,
    updatedAt: Date.now(),
    trackingPaused: false,
    ...(result.video?.duration ? { completedDuration: Math.round(result.video.duration) } : {})
  }
  if (result.progress !== undefined) next.progress = result.progress
  else if (result.status !== 'pending') delete next.progress
  if (result.error) next.lastError = result.error
  else delete next.lastError
  replaceTask(next)
  await persistTask(next)
  if (next.status === 'done') await loadCompletedVideo(next)
}

async function handlePollError(target: VideoStudioPollingTarget, error: unknown) {
  const item = gallery.value.find((entry) => entry.requestId === target.requestId)
  if (!item) return

  if (Date.now() - item.createdAt >= VIDEO_STUDIO_TASK_BINDING_MS) {
    await expirePendingTask(item)
    return
  }

  const next = {
    ...item,
    updatedAt: Date.now(),
    lastError: extractApiErrorMessage(error, t('videoStudio.trackingFailed'))
  }
  replaceTask(next)
  await persistTask(next)
}

async function expirePendingTask(item: VideoStudioGalleryItem) {
  poller.stop(item.requestId)
  const next: VideoStudioGalleryItem = {
    ...item,
    status: 'expired',
    trackingPaused: true,
    updatedAt: Date.now()
  }
  delete next.progress
  delete next.lastError
  replaceTask(next)
  await persistTask(next)
}

async function loadCompletedVideo(item: VideoStudioGalleryItem) {
  if (contentLoadingIds.value.has(item.id) || videoURLs[item.id]) return
  const controller = new AbortController()
  contentControllers.get(item.id)?.abort()
  contentControllers.set(item.id, controller)
  contentLoadingIds.value = new Set(contentLoadingIds.value).add(item.id)
  try {
    const videoBlob = await getVideoContent(item.apiKeyId, item.requestId, controller.signal)
    const next = { ...item, videoBlob, updatedAt: Date.now() }
    delete next.lastError
    replaceTask(next)
    await persistTask(next, true)
    createVideoURL(next)
  } catch (error: any) {
    if (!isCanceled(error)) {
      const next = { ...item, updatedAt: Date.now(), lastError: extractApiErrorMessage(error, t('videoStudio.contentLoadFailed')) }
      replaceTask(next)
      await persistTask(next)
    }
  } finally {
    contentControllers.delete(item.id)
    const ids = new Set(contentLoadingIds.value)
    ids.delete(item.id)
    contentLoadingIds.value = ids
  }
}

async function stopTracking(item: VideoStudioGalleryItem) {
  poller.pause(item.requestId)
  const next = { ...item, trackingPaused: true, updatedAt: Date.now() }
  replaceTask(next)
  await persistTask(next, true)
}

async function resumeTracking(item: VideoStudioGalleryItem) {
  if (!keyAvailable(item.apiKeyId)) return
  const next = { ...item, trackingPaused: false, updatedAt: Date.now() }
  delete next.lastError
  replaceTask(next)
  await persistTask(next, true)
  poller.track({ requestId: item.requestId, apiKeyId: item.apiKeyId }, true)
}

async function deleteItem(item: VideoStudioGalleryItem) {
  if (item.status === 'pending' && !window.confirm(t('videoStudio.removePendingConfirm'))) return
  poller.stop(item.requestId)
  contentControllers.get(item.id)?.abort()
  revokeVideoURL(item.id)
  const previous = gallery.value
  gallery.value = gallery.value.filter((entry) => entry.id !== item.id)
  try {
    await deleteVideoStudioGalleryItem(userId.value, item.id)
  } catch (error) {
    gallery.value = previous
    if (item.videoBlob) createVideoURL(item)
    appStore.showError(extractApiErrorMessage(error, t('videoStudio.galleryDeleteFailed')))
  }
}

async function clearGallery() {
  if (!window.confirm(t('videoStudio.clearConfirm'))) return
  const previous = gallery.value
  poller.stopAll()
  for (const item of gallery.value) {
    contentControllers.get(item.id)?.abort()
    revokeVideoURL(item.id)
  }
  gallery.value = []
  try {
    await clearVideoStudioGallery(userId.value)
  } catch (error) {
    gallery.value = previous
    for (const item of previous) if (item.videoBlob) createVideoURL(item)
    resumePersistedTasks()
    appStore.showError(extractApiErrorMessage(error, t('videoStudio.galleryClearFailed')))
  }
}

async function persistTask(item: VideoStudioGalleryItem, notifyFailure = false): Promise<VideoStudioGalleryItem | null> {
  try {
    return await saveVideoStudioGalleryItem(item)
  } catch (error) {
    if (notifyFailure) appStore.showWarning(extractApiErrorMessage(error, t('videoStudio.gallerySaveFailed')))
    return null
  }
}

function replaceTask(item: VideoStudioGalleryItem) {
  const index = gallery.value.findIndex((entry) => entry.id === item.id)
  if (index < 0) gallery.value = [item, ...gallery.value]
  else gallery.value = gallery.value.map((entry) => entry.id === item.id ? item : entry)
}

function createVideoURL(item: VideoStudioGalleryItem) {
  if (!item.videoBlob || typeof URL === 'undefined' || typeof URL.createObjectURL !== 'function') return
  revokeVideoURL(item.id)
  videoURLs[item.id] = URL.createObjectURL(item.videoBlob)
}

function revokeVideoURL(id: string) {
  const url = videoURLs[id]
  if (!url) return
  if (typeof URL !== 'undefined' && typeof URL.revokeObjectURL === 'function') URL.revokeObjectURL(url)
  delete videoURLs[id]
}

function keyAvailable(apiKeyId: number): boolean {
  return grokKeys.value.some((key) => key.id === apiKeyId)
}

function restoreSettings() {
  try {
    const saved = JSON.parse(localStorage.getItem(storageKey()) || '{}') as Partial<typeof form>
    if (typeof saved.apiKeyId === 'number') form.apiKeyId = saved.apiKeyId
    if (typeof saved.duration === 'number') form.duration = saved.duration
    if (VIDEO_STUDIO_ASPECT_RATIOS.includes(saved.aspectRatio as VideoStudioAspectRatio)) form.aspectRatio = saved.aspectRatio!
    if (VIDEO_STUDIO_RESOLUTIONS.includes(saved.resolution as VideoStudioResolution)) form.resolution = saved.resolution!
  } catch {
    localStorage.removeItem(storageKey())
  }
}

function persistSettings() {
  if (!userId.value) return
  try {
    localStorage.setItem(storageKey(), JSON.stringify({
      apiKeyId: form.apiKeyId,
      duration: form.duration,
      aspectRatio: form.aspectRatio,
      resolution: form.resolution
    }))
  } catch {
    // Settings persistence is optional.
  }
}

function clampDuration(value: number): number {
  const rounded = Number.isFinite(value) ? Math.round(value) : VIDEO_STUDIO_DURATIONS.default
  return Math.min(maxDuration.value, Math.max(minDuration.value, rounded))
}

function displayStatus(item: VideoStudioGalleryItem): string {
  return item.status === 'pending' && item.trackingPaused ? 'paused' : item.status
}

function elapsedSeconds(item: VideoStudioGalleryItem): number {
  return Math.max(0, Math.floor((now.value - item.createdAt) / 1000))
}

function ratioShape(value: string): 'square' | 'landscape' | 'portrait' {
  const [width, height] = value.split(':').map(Number)
  if (!width || !height || width === height) return 'square'
  return width > height ? 'landscape' : 'portrait'
}

function aspectRatioStyle(value: string): Record<string, string> {
  const [width, height] = value.split(':').map(Number)
  return width > 0 && height > 0 ? { aspectRatio: `${width} / ${height}` } : { aspectRatio: '16 / 9' }
}

function formatPrice(value: number): string {
  return new Intl.NumberFormat(locale.value || undefined, {
    style: 'currency',
    currency: pricing.value?.currency || 'USD',
    minimumFractionDigits: 2,
    maximumFractionDigits: 6
  }).format(value)
}

function formatCreatedAt(value: number): string {
  return new Intl.DateTimeFormat(locale.value || undefined, {
    month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit'
  }).format(value)
}

function downloadName(item: VideoStudioGalleryItem): string {
  return `video-studio-${new Date(item.createdAt).toISOString().replace(/[:.]/g, '-')}.mp4`
}

function isCanceled(error: any): boolean {
  return error?.code === 'ERR_CANCELED' || error?.name === 'AbortError' || error?.name === 'CanceledError'
}

watch(() => [form.apiKeyId, form.duration, form.aspectRatio, form.resolution], persistSettings)
watch(() => form.apiKeyId, () => {
  if (!loadingKeys.value) void loadCapabilities()
})
watch(() => form.duration, (duration, previousDuration) => {
  const clamped = clampDuration(duration)
  if (duration !== clamped) {
    form.duration = clamped
    return
  }
  if (duration !== previousDuration && capabilities.value) void loadPricing()
})

onMounted(async () => {
  elapsedTimer = setInterval(() => { now.value = Date.now() }, 1000)
  await loadGallery()
  await loadKeys()
  resumePersistedTasks()
})

onBeforeUnmount(() => {
  capabilitiesController?.abort()
  pricingController?.abort()
  generationController?.abort()
  for (const controller of contentControllers.values()) controller.abort()
  for (const id of Object.keys(videoURLs)) revokeVideoURL(id)
  if (elapsedTimer) clearInterval(elapsedTimer)
})
</script>

<style scoped>
.video-studio-shell { display: flex; min-height: calc(100vh - 8.5rem); flex-direction: column; gap: 1rem; }
.studio-status { display: flex; min-height: 2.75rem; align-items: center; gap: .75rem; border-bottom: 1px solid rgb(229 231 235); color: rgb(75 85 99); font-size: .8125rem; }
.studio-model-mark { display: flex; align-items: center; gap: .5rem; color: rgb(17 24 39); font-weight: 700; }
.studio-status-divider { height: 1rem; width: 1px; background: rgb(209 213 219); }
.studio-status-key { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.studio-price-summary { margin-left: auto; display: flex; align-items: baseline; gap: .5rem; white-space: nowrap; }
.studio-price-summary strong { color: rgb(5 150 105); font-size: .9375rem; }
.studio-grid { display: grid; min-width: 0; min-height: 36rem; width: 100%; flex: 1 0 auto; grid-template-columns: minmax(19rem, 21rem) minmax(0, 1fr); align-items: stretch; gap: 1.25rem; }
.studio-controls, .studio-workspace { min-width: 0; overflow: hidden; border: 1px solid rgb(229 231 235); border-radius: 8px; background: rgb(255 255 255 / .94); box-shadow: 0 1px 2px rgb(0 0 0 / .04); }
.studio-controls { display: flex; flex-direction: column; }
.studio-control-scroll { display: flex; flex-direction: column; gap: 1.125rem; padding: 1.25rem; }
.studio-workspace { display: flex; flex-direction: column; }
.control-group { display: flex; min-width: 0; flex-direction: column; gap: .5rem; }
.control-label { color: rgb(55 65 81); font-size: .75rem; font-weight: 700; }
.control-error { border: 1px solid rgb(254 202 202); border-radius: 6px; background: rgb(254 242 242); padding: .625rem .75rem; color: rgb(185 28 28); font-size: .75rem; line-height: 1.45; }
.studio-select, .studio-prompt { width: 100%; border: 1px solid rgb(209 213 219); border-radius: 6px; background: white; color: rgb(17 24 39); font-size: .875rem; outline: none; transition: border-color .15s, box-shadow .15s; }
.studio-select { height: 2.625rem; padding: 0 .75rem; }
.studio-select:focus, .studio-prompt:focus { border-color: rgb(13 148 136); box-shadow: 0 0 0 3px rgb(20 184 166 / .12); }
.ratio-select-shell { position: relative; }
.ratio-select { padding-left: 2.75rem; }
.ratio-shape { position: absolute; left: .875rem; top: 50%; z-index: 1; border: 1.5px solid currentColor; border-radius: 2px; color: rgb(107 114 128); pointer-events: none; transform: translateY(-50%); }
.ratio-shape.square { width: 1.2rem; height: 1.2rem; }
.ratio-shape.landscape { width: 1.45rem; height: .9rem; }
.ratio-shape.portrait { width: .85rem; height: 1.35rem; }
.stepper { display: grid; height: 2.625rem; grid-template-columns: 2.625rem minmax(0, 1fr) 2.625rem; overflow: hidden; border: 1px solid rgb(209 213 219); border-radius: 6px; }
.stepper button { color: rgb(75 85 99); font-size: 1rem; font-weight: 700; }
.stepper button:first-child { border-right: 1px solid rgb(229 231 235); }
.stepper button:last-child { border-left: 1px solid rgb(229 231 235); }
.stepper button:disabled { cursor: not-allowed; opacity: .35; }
.stepper output { display: flex; align-items: center; justify-content: center; color: rgb(17 24 39); font-size: .8125rem; font-weight: 700; font-variant-numeric: tabular-nums; }
.segmented-control { display: grid; overflow: hidden; border: 1px solid rgb(209 213 219); border-radius: 6px; background: rgb(249 250 251); }
.segmented-control.three-columns { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.segmented-control button { min-height: 2.375rem; border-right: 1px solid rgb(209 213 219); padding: .375rem .35rem; color: rgb(75 85 99); font-size: .72rem; font-weight: 600; }
.segmented-control button:last-child { border-right: 0; }
.segmented-control button.active { background: white; color: rgb(15 118 110); box-shadow: inset 0 -2px 0 rgb(13 148 136); }
.segmented-control button:disabled { cursor: not-allowed; opacity: .4; }
.studio-price-area { margin-top: auto; display: flex; align-items: flex-end; justify-content: space-between; gap: .75rem; border-top: 1px solid rgb(229 231 235); padding: 1rem 1.25rem; }
.price-detail { min-width: 0; color: rgb(107 114 128); font-size: .6875rem; }
.studio-price-area strong { flex: 0 0 auto; color: rgb(5 150 105); font-size: .9375rem; }
.prompt-workspace { min-width: 0; overflow: hidden; }
.prompt-header { display: flex; min-height: 3rem; align-items: center; justify-content: space-between; padding: 0 1.25rem; }
.prompt-header > span { color: rgb(156 163 175); font-size: .6875rem; font-variant-numeric: tabular-nums; }
.studio-prompt { display: block; min-height: 16rem; max-height: 40rem; width: calc(100% - 2.5rem); margin: 0 1.25rem; resize: vertical; padding: .875rem; line-height: 1.65; }
.prompt-submit-area { display: flex; min-height: 4.25rem; align-items: center; justify-content: space-between; gap: 1rem; padding: .75rem 1.25rem 1rem; }
.prompt-selection { min-width: 0; overflow: hidden; color: rgb(107 114 128); font-size: .75rem; text-overflow: ellipsis; white-space: nowrap; }
.generate-button { display: inline-flex; min-height: 2.625rem; flex: 0 0 auto; align-items: center; justify-content: center; gap: .5rem; border-radius: 6px; background: rgb(15 118 110); padding: .625rem 1rem; color: white; font-size: .8125rem; font-weight: 700; }
.generate-button:hover:not(:disabled) { background: rgb(17 94 89); }
.generate-button:disabled { cursor: not-allowed; opacity: .45; }
.studio-gallery { min-height: 20rem; border-top: 1px solid rgb(229 231 235); }
.gallery-header { display: flex; min-height: 4.5rem; align-items: center; justify-content: space-between; gap: 1rem; padding: .875rem 1.25rem; }
.gallery-header h2 { color: rgb(17 24 39); font-size: .9rem; font-weight: 700; }
.gallery-header p { margin-top: .15rem; color: rgb(107 114 128); font-size: .6875rem; }
.toolbar-icon-button { display: inline-flex; width: 2.25rem; height: 2.25rem; align-items: center; justify-content: center; border: 1px solid rgb(229 231 235); border-radius: 6px; color: rgb(107 114 128); }
.toolbar-icon-button:hover { border-color: rgb(252 165 165); color: rgb(220 38 38); }
.gallery-empty { display: flex; min-height: 18rem; flex-direction: column; align-items: center; justify-content: center; gap: .6rem; padding: 2rem; text-align: center; }
.gallery-empty h3 { color: rgb(31 41 55); font-size: .9rem; font-weight: 700; }
.gallery-empty p { max-width: 28rem; color: rgb(107 114 128); font-size: .75rem; }
.empty-icon { display: flex; width: 2.75rem; height: 2.75rem; align-items: center; justify-content: center; border: 1px solid rgb(209 213 219); border-radius: 50%; color: rgb(107 114 128); }
.gallery-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(100%, 18rem), 1fr)); gap: 1rem; padding: 0 1.25rem 1.25rem; }
.video-item { min-width: 0; overflow: hidden; border: 1px solid rgb(229 231 235); border-radius: 8px; background: white; }
.video-frame { position: relative; width: 100%; min-height: 10rem; overflow: hidden; background: rgb(17 24 39); }
.video-frame video { display: block; width: 100%; height: 100%; object-fit: contain; }
.video-state { position: absolute; inset: 0; display: flex; min-width: 0; flex-direction: column; align-items: center; justify-content: center; gap: .5rem; padding: 1rem; color: rgb(229 231 235); text-align: center; }
.video-state strong { font-size: .8rem; }
.video-state small { display: -webkit-box; max-width: 100%; overflow: hidden; color: rgb(156 163 175); font-size: .68rem; line-height: 1.45; -webkit-box-orient: vertical; -webkit-line-clamp: 3; }
.video-state.state-failed, .video-state.state-expired { background: rgb(69 10 10 / .45); color: rgb(254 202 202); }
.video-state.state-paused { background: rgb(31 41 55); }
.video-progress { display: flex; width: min(12rem, 80%); align-items: center; gap: .625rem; }
.video-progress-track { height: .35rem; min-width: 0; flex: 1; overflow: hidden; border-radius: 999px; background: rgb(156 163 175 / .3); }
.video-progress-track span { display: block; height: 100%; border-radius: inherit; background: rgb(20 184 166); transition: width .2s ease; }
.video-progress strong { width: 2.4rem; flex: 0 0 auto; text-align: right; font-variant-numeric: tabular-nums; }
.state-action { margin-top: .25rem; border: 1px solid rgb(107 114 128); border-radius: 5px; padding: .35rem .65rem; color: white; font-size: .6875rem; font-weight: 600; }
.video-item-meta { display: flex; min-width: 0; align-items: center; gap: .75rem; padding: .75rem; }
.video-copy { min-width: 0; flex: 1; }
.video-copy p { overflow: hidden; color: rgb(31 41 55); font-size: .75rem; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.video-copy span { display: block; margin-top: .2rem; overflow: hidden; color: rgb(107 114 128); font-size: .625rem; text-overflow: ellipsis; white-space: nowrap; }
.video-actions { display: flex; flex: 0 0 auto; align-items: center; gap: .25rem; }
.video-actions button, .video-actions a { display: inline-flex; width: 2rem; height: 2rem; align-items: center; justify-content: center; border-radius: 5px; color: rgb(107 114 128); }
.video-actions button:hover, .video-actions a:hover { background: rgb(243 244 246); color: rgb(15 118 110); }
.video-actions button:disabled { cursor: not-allowed; opacity: .35; }
.loading-ring { width: 1.45rem; height: 1.45rem; border: 2px solid rgb(156 163 175 / .35); border-top-color: rgb(20 184 166); border-radius: 50%; animation: spin .8s linear infinite; }
.loading-ring.small { width: 1rem; height: 1rem; border-color: rgb(255 255 255 / .4); border-top-color: white; }
@keyframes spin { to { transform: rotate(360deg); } }

@media (max-width: 900px) {
  .video-studio-shell { min-height: auto; }
  .studio-grid { grid-template-columns: minmax(0, 1fr); }
  .studio-controls { overflow: visible; }
}

@media (max-width: 520px) {
  .studio-status { gap: .5rem; }
  .studio-status-divider { display: none; }
  .studio-model-mark span { display: none; }
  .studio-price-summary span { display: none; }
  .studio-grid { gap: .75rem; }
  .studio-control-scroll, .gallery-grid { padding-inline: .875rem; }
  .studio-prompt { width: calc(100% - 1.75rem); margin-inline: .875rem; }
  .prompt-header, .prompt-submit-area, .gallery-header { padding-inline: .875rem; }
  .prompt-submit-area { align-items: stretch; flex-direction: column; }
  .generate-button { width: 100%; }
  .prompt-selection { white-space: normal; }
}

:global(.dark .studio-status) { border-color: rgb(51 65 85); color: rgb(148 163 184); }
:global(.dark .studio-model-mark), :global(.dark .gallery-header h2), :global(.dark .gallery-empty h3) { color: rgb(226 232 240); }
:global(.dark .studio-status-divider) { background: rgb(71 85 105); }
:global(.dark .studio-controls), :global(.dark .studio-workspace), :global(.dark .video-item) { border-color: rgb(51 65 85); background: rgb(15 23 42 / .94); }
:global(.dark .control-label), :global(.dark .stepper output), :global(.dark .video-copy p) { color: rgb(203 213 225); }
:global(.dark .studio-select), :global(.dark .studio-prompt) { border-color: rgb(71 85 105); background: rgb(15 23 42); color: rgb(226 232 240); }
:global(.dark .stepper), :global(.dark .segmented-control), :global(.dark .studio-price-area), :global(.dark .studio-gallery), :global(.dark .toolbar-icon-button) { border-color: rgb(51 65 85); }
:global(.dark .stepper button:first-child), :global(.dark .stepper button:last-child), :global(.dark .segmented-control button) { border-color: rgb(51 65 85); }
:global(.dark .segmented-control) { background: rgb(15 23 42); }
:global(.dark .segmented-control button) { color: rgb(148 163 184); }
:global(.dark .segmented-control button.active) { background: rgb(30 41 59); color: rgb(94 234 212); }
:global(.dark .control-error) { border-color: rgb(127 29 29); background: rgb(69 10 10 / .35); color: rgb(252 165 165); }
:global(.dark .video-actions button:hover), :global(.dark .video-actions a:hover) { background: rgb(30 41 59); color: rgb(94 234 212); }
</style>
