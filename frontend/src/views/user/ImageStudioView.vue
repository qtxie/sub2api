<template>
  <AppLayout>
    <div class="image-studio-shell">
      <div class="studio-status" aria-live="polite">
        <div class="studio-model-mark">
          <Icon name="sparkles" size="sm" />
          <span>{{ selectedModelCapability?.label || t('imageStudio.model') }}</span>
        </div>
        <span class="studio-status-divider"></span>
        <span class="studio-status-key">{{ selectedKey?.name || t('imageStudio.selectApiKey') }}</span>
        <div class="studio-price-summary" data-testid="image-pricing">
          <span>{{ t('imageStudio.estimate') }}</span>
          <strong>{{ formattedEstimate }}</strong>
        </div>
      </div>

      <form class="studio-grid" @submit.prevent="generate">
        <aside class="studio-controls" :aria-label="t('imageStudio.controls')">
          <div class="studio-control-scroll">
            <div class="control-group">
              <label for="image-studio-key" class="control-label">{{ t('imageStudio.apiKey') }}</label>
              <select
                id="image-studio-key"
                v-model.number="form.apiKeyId"
                class="studio-select"
                :disabled="loadingKeys || imageKeys.length === 0"
              >
                <option :value="0" disabled>{{ t('imageStudio.selectApiKey') }}</option>
                <option v-for="key in imageKeys" :key="key.id" :value="key.id">{{ key.name }}</option>
              </select>
            </div>

            <div v-if="capabilitiesError" class="capabilities-error" role="alert">
              {{ capabilitiesError }}
            </div>

            <div v-if="capabilities" class="control-group">
              <label for="image-studio-model" class="control-label">{{ t('imageStudio.model') }}</label>
              <select
                id="image-studio-model"
                v-model="form.model"
                class="studio-select"
                :disabled="loadingCapabilities"
              >
                <option v-for="model in capabilities.models" :key="model.id" :value="model.id">
                  {{ model.label }}
                </option>
              </select>
            </div>

            <div v-if="provider === 'openai'" class="control-group">
              <label for="image-studio-size" class="control-label">{{ t('imageStudio.size') }}</label>
              <div class="size-select-shell">
                <span class="size-shape" :class="selectedSizeShape" aria-hidden="true"></span>
                <select
                  id="image-studio-size"
                  v-model="sizeMode"
                  class="studio-select size-select"
                  @change="applySizeMode"
                >
                  <option value="auto">{{ t('imageStudio.sizeAuto') }}</option>
                  <option v-for="option in sizeOptions" :key="option.value" :value="option.value">
                    {{ option.label }} · {{ option.detail }}{{ option.experimental ? ` · ${t('imageStudio.experimental')}` : '' }}
                  </option>
                  <option value="custom">{{ t('imageStudio.customSize') }}</option>
                </select>
              </div>
              <div v-if="sizeMode === 'custom'" class="custom-size-editor">
                <label class="custom-size-field" for="image-studio-width">
                  <span>{{ t('imageStudio.width') }}</span>
                  <span class="dimension-input-shell">
                    <input
                      id="image-studio-width"
                      v-model.number="customSize.width"
                      type="number"
                      :min="customWidthBounds?.min || 16"
                      :max="customWidthBounds?.max || 3840"
                      step="16"
                      inputmode="numeric"
                      :aria-invalid="!sizeValidation.valid"
                      @input="syncCustomSize"
                      @blur="normalizeCustomDimension('width')"
                    />
                    <small>px</small>
                  </span>
                </label>
                <button
                  type="button"
                  class="swap-size-button"
                  :title="t('imageStudio.swapDimensions')"
                  @click="swapCustomSize"
                >
                  <Icon name="swap" size="sm" />
                </button>
                <label class="custom-size-field" for="image-studio-height">
                  <span>{{ t('imageStudio.height') }}</span>
                  <span class="dimension-input-shell">
                    <input
                      id="image-studio-height"
                      v-model.number="customSize.height"
                      type="number"
                      :min="customHeightBounds?.min || 16"
                      :max="customHeightBounds?.max || 3840"
                      step="16"
                      inputmode="numeric"
                      :aria-invalid="!sizeValidation.valid"
                      @input="syncCustomSize"
                      @blur="normalizeCustomDimension('height')"
                    />
                    <small>px</small>
                  </span>
                </label>
              </div>
              <p v-if="sizeErrorMessage" class="size-feedback error" role="alert">{{ sizeErrorMessage }}</p>
              <p v-else-if="sizeValidation.experimental" class="size-feedback experimental">
                <Icon name="exclamationCircle" size="xs" />
                <span>{{ t('imageStudio.experimentalSize') }}</span>
              </p>
              <p v-else-if="sizeWasAdjusted" class="size-feedback adjusted" role="status">
                <Icon name="checkCircle" size="xs" />
                <span>{{ t('imageStudio.sizeAdjusted') }}</span>
              </p>
            </div>

            <div v-if="provider !== 'openai' && !(provider === 'grok' && sourceImages.length === 1) && aspectRatioOptions.length" class="control-group">
              <label for="image-studio-aspect-ratio" class="control-label">{{ t('imageStudio.aspectRatio') }}</label>
              <div class="size-select-shell">
                <span class="size-shape" :class="selectedSizeShape" aria-hidden="true"></span>
                <select
                  id="image-studio-aspect-ratio"
                  v-model="form.aspectRatio"
                  class="studio-select size-select"
                >
                  <option v-for="ratio in aspectRatioOptions" :key="ratio" :value="ratio">
                    {{ ratio === 'auto' ? t('imageStudio.aspectRatioAuto') : ratio }}
                  </option>
                </select>
              </div>
            </div>

            <div v-if="provider === 'gemini' && imageSizeOptions.length" class="control-group">
              <label for="image-studio-image-size" class="control-label">{{ t('imageStudio.imageSize') }}</label>
              <select id="image-studio-image-size" v-model="form.imageSize" class="studio-select">
                <option v-for="size in imageSizeOptions" :key="size" :value="size">{{ size }}</option>
              </select>
            </div>

            <div v-if="provider === 'grok' && !hasSourceImages && resolutionOptions.length" class="control-group">
              <label for="image-studio-resolution" class="control-label">{{ t('imageStudio.resolution') }}</label>
              <select id="image-studio-resolution" v-model="form.resolution" class="studio-select">
                <option v-for="resolution in resolutionOptions" :key="resolution" :value="resolution">
                  {{ resolution.toUpperCase() }}
                </option>
              </select>
            </div>

            <fieldset v-if="qualityOptions.length && !(provider === 'grok' && hasSourceImages)" class="control-group">
              <legend class="control-label">{{ t('imageStudio.quality') }}</legend>
              <div class="segmented-control" :class="segmentColumnClass(qualityOptions.length)">
                <button
                  v-for="option in qualityOptions"
                  :key="option.value"
                  type="button"
                  :class="{ active: form.quality === option.value }"
                  :aria-pressed="form.quality === option.value"
                  @click="form.quality = option.value"
                >{{ option.label }}</button>
              </div>
            </fieldset>

            <fieldset v-if="backgroundOptions.length" class="control-group background-control">
              <legend class="control-label">{{ t('imageStudio.background') }}</legend>
              <div class="segmented-control two-columns">
                <button
                  v-for="option in backgroundOptions"
                  :key="option.value"
                  type="button"
                  :class="{ active: form.background === option.value }"
                  :aria-pressed="form.background === option.value"
                  @click="form.background = option.value"
                >{{ option.label }}</button>
              </div>
            </fieldset>

            <fieldset v-if="outputFormats.length" class="control-group min-w-0">
              <legend class="control-label">{{ t('imageStudio.format') }}</legend>
              <div class="segmented-control three-columns compact">
                <button
                  v-for="format in outputFormats"
                  :key="format"
                  type="button"
                  :class="{ active: form.outputFormat === format }"
                  :aria-pressed="form.outputFormat === format"
                  @click="form.outputFormat = format"
                >{{ format.toUpperCase() }}</button>
              </div>
            </fieldset>

            <div v-if="maxImageCount > 1 && !(provider === 'grok' && hasSourceImages)" class="control-group count-control">
              <span class="control-label">{{ t('imageStudio.count') }}</span>
              <div class="stepper">
                <button type="button" :title="t('imageStudio.decreaseCount')" :disabled="form.count <= 1" @click="form.count--">-</button>
                <output>{{ form.count }}</output>
                <button type="button" :title="t('imageStudio.increaseCount')" :disabled="form.count >= maxImageCount" @click="form.count++">+</button>
              </div>
            </div>
          </div>

          <div class="studio-price-area">
            <div class="price-detail">
              <span v-if="loadingPricing">{{ t('common.loading') }}</span>
              <span v-else-if="selectedUnitPrice != null">{{ t('imageStudio.perImage', { price: formatPrice(selectedUnitPrice) }) }}</span>
              <span v-else-if="pricing?.pricing_kind === 'usage_based'">{{ t('imageStudio.usageBasedPricing') }}</span>
              <span v-else>{{ pricingError || t('imageStudio.pricingUnavailable') }}</span>
            </div>
            <strong>{{ formattedEstimate }}</strong>
          </div>
        </aside>

        <div class="studio-workspace">
          <section class="prompt-workspace" aria-labelledby="image-studio-prompt-label">
            <div class="prompt-header">
              <label id="image-studio-prompt-label" for="image-studio-prompt" class="control-label">{{ t('imageStudio.prompt') }}</label>
              <span>{{ form.prompt.length }}</span>
            </div>
            <textarea
              id="image-studio-prompt"
              v-model="form.prompt"
              class="studio-prompt"
              :placeholder="t('imageStudio.promptPlaceholder')"
              rows="10"
            ></textarea>
            <div v-if="maxInputImageCount > 0" class="source-images-control">
              <div class="source-images-header">
                <div class="source-images-label">
                  <span class="control-label">{{ t('imageStudio.sourceImages') }}</span>
                  <small>{{ t('imageStudio.sourceImagesCount', { count: sourceImages.length, max: maxInputImageCount }) }}</small>
                </div>
                <label
                  class="source-image-upload-button"
                  :class="{ disabled: readingSourceImages || sourceImages.length >= maxInputImageCount }"
                >
                  <Icon name="upload" size="sm" />
                  <span>{{ t('imageStudio.addSourceImages') }}</span>
                  <input
                    data-testid="source-image-input"
                    type="file"
                    accept="image/png,image/jpeg,image/webp"
                    :multiple="maxInputImageCount > 1"
                    class="sr-only"
                    :aria-label="t('imageStudio.addSourceImages')"
                    :disabled="readingSourceImages || sourceImages.length >= maxInputImageCount"
                    @change="handleSourceImageFiles"
                  />
                </label>
              </div>
              <div v-if="sourceImages.length" class="source-image-list">
                <article v-for="image in sourceImages" :key="image.id" class="source-image-item">
                  <button
                    type="button"
                    class="source-image-preview-button"
                    :title="t('imageStudio.previewSourceImage')"
                    @click="openSourceImagePreview(image)"
                  >
                    <img :src="sourceImageURL(image)" :alt="image.name" />
                  </button>
                  <button
                    type="button"
                    class="source-image-remove-button"
                    :title="t('imageStudio.removeSourceImage')"
                    :aria-label="t('imageStudio.removeSourceImage')"
                    @click="removeSourceImage(image.id)"
                  >
                    <Icon name="x" size="xs" />
                  </button>
                  <span :title="image.name">{{ image.name }}</span>
                </article>
              </div>
              <small class="source-images-hint">{{ t('imageStudio.sourceImagesHint') }}</small>
            </div>
            <div class="prompt-submit-area">
              <div class="prompt-request-summary">
                <span class="prompt-selection">{{ selectionSummary }}</span>
                <span v-if="activeGenerationCount > 0" class="active-generation-count" role="status">
                  <span class="loading-ring tiny"></span>
                  {{ t('imageStudio.activeGenerations', { count: activeGenerationCount }) }}
                </span>
              </div>
              <button type="submit" class="generate-button" :disabled="!canGenerate">
                <Icon name="sparkles" size="sm" />
                <span>{{ t('imageStudio.generate') }}</span>
              </button>
            </div>
          </section>

          <section class="studio-gallery" aria-labelledby="image-studio-gallery-title">
            <header class="gallery-header">
              <div>
                <h2 id="image-studio-gallery-title">{{ t('imageStudio.gallery') }}</h2>
                <p>
                  {{ galleryMode === 'results'
                    ? t('imageStudio.resultsCount', { count: gallery.length })
                    : t('imageStudio.archiveCount', { count: archives.length }) }}
                </p>
              </div>
              <div class="gallery-header-actions">
                <div class="gallery-mode-switch" :aria-label="t('imageStudio.gallery')">
                  <button
                    type="button"
                    :aria-pressed="galleryMode === 'results'"
                    :class="{ active: galleryMode === 'results' }"
                    @click="galleryMode = 'results'"
                  >
                    {{ t('imageStudio.results') }}
                    <span>{{ gallery.length }}</span>
                  </button>
                  <button
                    type="button"
                    :aria-pressed="galleryMode === 'archive'"
                    :class="{ active: galleryMode === 'archive' }"
                    @click="galleryMode = 'archive'"
                  >
                    {{ t('imageStudio.archive') }}
                    <span>{{ archives.length }}</span>
                  </button>
                </div>
                <button
                  v-if="galleryMode === 'results' && gallery.length > 0"
                  type="button"
                  class="toolbar-icon-button"
                  :title="t('imageStudio.clearGallery')"
                  @click="clearResults"
                >
                  <Icon name="trash" size="sm" />
                </button>
                <button
                  v-if="galleryMode === 'archive' && archives.length > 0"
                  type="button"
                  class="toolbar-icon-button"
                  :disabled="loadingGallery || archivingIds.size > 0 || deletingArchiveIds.size > 0 || clearingArchive"
                  :title="t('imageStudio.clearArchive')"
                  @click="clearArchive"
                >
                  <Icon name="trash" size="sm" />
                </button>
              </div>
            </header>

            <div v-if="galleryMode === 'results' && !loadingKeys && imageKeys.length === 0" class="gallery-empty">
              <span class="empty-icon"><Icon name="key" size="lg" /></span>
              <h3>{{ t('imageStudio.noKeysTitle') }}</h3>
              <p>{{ t('imageStudio.noEligibleKeys') }}</p>
              <router-link to="/keys" class="btn btn-primary">
                <Icon name="key" size="sm" class="mr-2" />
                {{ t('imageStudio.manageKeys') }}
              </router-link>
            </div>

            <div v-else-if="galleryMode === 'archive' && loadingGallery && archives.length === 0" class="gallery-empty">
              <span class="loading-ring"></span>
              <p>{{ t('common.loading') }}</p>
            </div>

            <div v-else-if="galleryMode === 'results' && gallery.length === 0 && generationJobs.length === 0" class="gallery-empty">
              <span class="empty-icon"><Icon name="sparkles" size="lg" /></span>
              <h3>{{ t('imageStudio.emptyTitle') }}</h3>
              <p>{{ t('imageStudio.emptyGallery') }}</p>
            </div>

            <div v-else-if="galleryMode === 'archive' && archives.length === 0" class="gallery-empty">
              <span class="empty-icon"><Icon name="inbox" size="lg" /></span>
              <h3>{{ t('imageStudio.archiveEmptyTitle') }}</h3>
              <p>{{ t('imageStudio.archiveEmpty') }}</p>
            </div>

            <div v-else class="gallery-grid">
              <template v-if="galleryMode === 'results'">
                <article
                  v-for="job in generationJobs"
                  :key="job.id"
                  class="generation-placeholder"
                  :style="aspectRatioStyle(job.snapshot.aspectToken)"
                  :data-job-id="job.id"
                  data-testid="generation-placeholder"
                >
                  <template v-if="job.status === 'generating'">
                    <span class="loading-ring"></span>
                    <strong>
                      {{ job.snapshot.expectedCount > 1
                        ? t('imageStudio.generatingCount', { count: job.snapshot.expectedCount })
                        : t('imageStudio.generating') }}
                    </strong>
                    <small :id="`generation-prompt-${job.id}`" class="placeholder-prompt">{{ job.snapshot.prompt }}</small>
                    <small v-if="job.snapshot.sourceImages.length">{{ t('imageStudio.sourceImagesAttached', { count: job.snapshot.sourceImages.length }) }}</small>
                    <small>{{ t('imageStudio.elapsed', { seconds: elapsedForJob(job) }) }}</small>
                    <button
                      type="button"
                      class="placeholder-icon-button"
                      :title="t('imageStudio.cancel')"
                      :aria-label="t('imageStudio.cancel')"
                      :aria-describedby="`generation-prompt-${job.id}`"
                      @click="cancelGeneration(job.id)"
                    >
                      <Icon name="x" size="sm" />
                    </button>
                  </template>
                  <template v-else>
                    <Icon name="exclamationCircle" size="lg" />
                    <strong>{{ t('imageStudio.generationFailed') }}</strong>
                    <small :id="`generation-prompt-${job.id}`" class="placeholder-prompt">{{ job.snapshot.prompt }}</small>
                    <small v-if="job.snapshot.sourceImages.length">{{ t('imageStudio.sourceImagesAttached', { count: job.snapshot.sourceImages.length }) }}</small>
                    <small>{{ job.error }}</small>
                    <div class="placeholder-actions">
                      <button
                        type="button"
                        class="retry-button"
                        :aria-describedby="`generation-prompt-${job.id}`"
                        @click="retryGeneration(job.id)"
                      >
                        <Icon name="refresh" size="sm" />
                        <span>{{ t('imageStudio.retry') }}</span>
                      </button>
                      <button
                        type="button"
                        class="placeholder-icon-button"
                        :title="t('imageStudio.dismissFailed')"
                        :aria-label="t('imageStudio.dismissFailed')"
                        :aria-describedby="`generation-prompt-${job.id}`"
                        @click="dismissGeneration(job.id)"
                      >
                        <Icon name="x" size="sm" />
                      </button>
                    </div>
                  </template>
                </article>

                <article v-for="item in gallery" :key="item.id" class="gallery-item">
                  <button
                    type="button"
                    class="gallery-image-button"
                    :style="aspectRatioStyle(item.aspectRatio || item.size)"
                    :title="t('imageStudio.preview')"
                    @click="openGalleryPreview(item)"
                  >
                    <img :src="item.imageSrc" :alt="item.prompt" loading="lazy" />
                  </button>
                  <div class="gallery-item-meta">
                    <div class="min-w-0">
                      <p>{{ item.prompt }}</p>
                      <span>{{ formatCreatedAt(item.createdAt) }} · {{ galleryItemSummary(item) }}</span>
                    </div>
                    <div class="gallery-actions">
                      <button type="button" :title="t('imageStudio.reuseSettings')" @click="reuseItem(item)">
                        <Icon name="refresh" size="sm" />
                      </button>
                      <a :href="item.imageSrc" :download="downloadName(item)" :title="t('imageStudio.download')" rel="noopener noreferrer">
                        <Icon name="download" size="sm" />
                      </a>
                      <button
                        type="button"
                        class="archive-button"
                        :class="{ archived: isArchived(item.id) }"
                        :disabled="loadingGallery || clearingArchive || isArchived(item.id) || isArchiving(item.id)"
                        :title="isArchived(item.id) ? t('imageStudio.archived') : isArchiving(item.id) ? t('imageStudio.archiving') : t('imageStudio.archiveImage')"
                        @click="archiveItem(item)"
                      >
                        <span v-if="isArchiving(item.id)" class="loading-ring tiny"></span>
                        <Icon v-else :name="isArchived(item.id) ? 'check' : 'inbox'" size="sm" />
                      </button>
                      <button type="button" :title="t('imageStudio.removeResult')" @click="removeResult(item)">
                        <Icon name="trash" size="sm" />
                      </button>
                    </div>
                  </div>
                </article>
              </template>

              <template v-else>
                <article v-for="item in archives" :key="item.id" class="gallery-item archived-gallery-item">
                  <button
                    v-if="item.thumbnailSrc"
                    type="button"
                    class="gallery-image-button"
                    :style="aspectRatioStyle(item.aspectRatio || item.size)"
                    :title="t('imageStudio.previewArchive')"
                    @click="openGalleryPreview(item)"
                  >
                    <img :src="item.thumbnailSrc" :alt="item.prompt" loading="lazy" />
                    <span class="archive-badge">{{ t('imageStudio.archiveThumbnail') }}</span>
                  </button>
                  <div v-else class="archive-thumbnail-unavailable" :style="aspectRatioStyle(item.aspectRatio || item.size)">
                    <Icon name="inbox" size="lg" />
                    <span>{{ t('imageStudio.thumbnailUnavailable') }}</span>
                  </div>
                  <div class="gallery-item-meta">
                    <div class="min-w-0">
                      <p>{{ item.prompt }}</p>
                      <span>{{ formatCreatedAt(item.archivedAt) }} · {{ galleryItemSummary(item) }}</span>
                    </div>
                    <div class="gallery-actions">
                      <button type="button" :title="t('imageStudio.reuseSettings')" @click="reuseItem(item)">
                        <Icon name="refresh" size="sm" />
                      </button>
                      <button type="button" :title="t('imageStudio.deleteArchive')" @click="deleteArchive(item)">
                        <Icon name="trash" size="sm" />
                      </button>
                    </div>
                  </div>
                </article>
              </template>
            </div>
          </section>
        </div>
      </form>
    </div>

    <div v-if="previewItem || previewSourceImage" class="preview-backdrop" role="dialog" aria-modal="true" @click.self="closePreview">
      <div class="preview-dialog">
        <button type="button" class="preview-close" :title="t('imageStudio.closePreview')" @click="closePreview">
          <Icon name="x" size="md" />
        </button>
        <img :src="previewSource" :alt="previewAlt" />
        <div class="preview-caption">
          <p>{{ previewCaption }}</p>
          <span>{{ previewDetail }}</span>
        </div>
      </div>
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
  generateImage,
  getImageStudioCapabilities,
  getImageStudioPricing,
  type ImageBackground,
  type ImageOutputFormat,
  type ImageQuality,
  type ImageStudioCapabilitiesResponse,
  type ImageStudioGenerationRequest,
  type ImageStudioImage,
  type ImageStudioModelCapability,
  type ImageStudioPricingResponse,
  type ImageStudioProvider,
  type ImageStudioSourceImage
} from '@/api/imageStudio'
import type { ApiKey } from '@/types'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  clearImageStudioGallery,
  createImageStudioArchiveItem,
  createImageStudioThumbnail,
  deleteImageStudioGalleryItem,
  isImageStudioArchiveLimitError,
  listImageStudioGallery,
  sanitizeImageStudioSource,
  saveImageStudioGalleryItem,
  type ImageStudioArchiveItem,
  type ImageStudioGalleryItem
} from '@/utils/imageStudioGallery'
import {
  GPT_IMAGE_2_SIZE_PRESETS,
  gptImage2DimensionBounds,
  imageBillingTierForSize,
  normalizeGPTImage2Dimension,
  snapGPTImage2Edge,
  validateGPTImage2Size
} from '@/utils/gptImage2'

interface ImageGenerationSettings {
  userId: number
  apiKeyId: number
  provider: ImageStudioProvider
  model: string
  prompt: string
  size: string
  aspectRatio: string
  imageSize: string
  resolution: string
  quality: ImageQuality
  background: ImageBackground
  outputFormat: ImageOutputFormat
  count: number
  expectedCount: number
  aspectToken: string
  sourceImages: ImageStudioSourceImage[]
}

interface ImageGenerationSnapshot extends ImageGenerationSettings {
  payload: ImageStudioGenerationRequest
}

interface ImageGenerationJob {
  id: string
  status: 'generating' | 'error'
  error: string
  startedAt: number
  snapshot: ImageGenerationSnapshot
}

interface SourceImageDraft extends ImageStudioSourceImage {
  id: string
  name: string
  size: number
}

const sourceImageMimeTypes = ['image/png', 'image/jpeg', 'image/webp'] as const
const maxSourceImageBytes = 6 * 1024 * 1024
// Base64 expands binary data by roughly 4/3. A 14 MB binary cap keeps the inline JSON below 20 MB.
const maxSourceImagesTotalBytes = 14 * 1024 * 1024

const { t, locale } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
const userId = computed(() => authStore.user?.id || 0)

const imageKeys = ref<ApiKey[]>([])
const gallery = ref<ImageStudioGalleryItem[]>([])
const archives = ref<ImageStudioArchiveItem[]>([])
const galleryMode = ref<'results' | 'archive'>('results')
const archivingIds = ref<Set<string>>(new Set())
const deletingArchiveIds = ref<Set<string>>(new Set())
const clearingArchive = ref(false)
const capabilities = ref<ImageStudioCapabilitiesResponse | null>(null)
const loadingKeys = ref(true)
const loadingGallery = ref(true)
const loadingCapabilities = ref(false)
const capabilitiesError = ref('')
const loadingPricing = ref(false)
const pricing = ref<ImageStudioPricingResponse | null>(null)
const pricingError = ref('')
const previewItem = ref<ImageStudioGalleryItem | ImageStudioArchiveItem | null>(null)
const previewSourceImage = ref<SourceImageDraft | null>(null)
const sourceImages = ref<SourceImageDraft[]>([])
const readingSourceImages = ref(false)
const generationJobs = ref<ImageGenerationJob[]>([])
const generationNow = ref(Date.now())
const generationControllers = new Map<string, AbortController>()
let capabilitiesController: AbortController | null = null
let pricingController: AbortController | null = null
let generationTimer: number | null = null
let generationSequence = 0
let viewUnmounted = false

const form = reactive({
  apiKeyId: 0,
  prompt: '',
  model: '',
  size: 'auto',
  aspectRatio: '',
  imageSize: '',
  resolution: '',
  quality: 'auto' as ImageQuality,
  background: 'auto' as ImageBackground,
  outputFormat: 'png' as ImageOutputFormat,
  count: 1
})

const sizeMode = ref('auto')
const customSize = reactive<{ width: number | string; height: number | string }>({ width: 1536, height: 1024 })
const lastValidCustomSize = reactive({ width: 1536, height: 1024 })
const sizeWasAdjusted = ref(false)

const sizeOptions = computed(() => [
  { value: '1024x1024', label: t('imageStudio.square'), detail: '1024 x 1024 · 1K', shape: 'square' },
  { value: '1536x1024', label: t('imageStudio.landscape'), detail: '1536 x 1024 · 2K', shape: 'landscape' },
  { value: '1024x1536', label: t('imageStudio.portrait'), detail: '1024 x 1536 · 2K', shape: 'portrait' },
  { value: '2048x2048', label: t('imageStudio.square'), detail: '2048 x 2048 · 2K', shape: 'square', experimental: true },
  { value: '2048x1152', label: t('imageStudio.landscape'), detail: '2048 x 1152 · 2K', shape: 'landscape' },
  { value: '1152x2048', label: t('imageStudio.portrait'), detail: '1152 x 2048 · 2K', shape: 'portrait' },
  { value: '3840x2160', label: t('imageStudio.landscape'), detail: '3840 x 2160 · 4K', shape: 'landscape', experimental: true },
  { value: '2160x3840', label: t('imageStudio.portrait'), detail: '2160 x 3840 · 4K', shape: 'portrait', experimental: true }
])
const selectedKey = computed(() => imageKeys.value.find((key) => key.id === form.apiKeyId))
const provider = computed<ImageStudioProvider | null>(() => capabilities.value?.provider || null)
const selectedModelCapability = computed<ImageStudioModelCapability | null>(() => (
  capabilities.value?.models.find((model) => model.id === form.model) || null
))
const aspectRatioOptions = computed(() => selectedModelCapability.value?.aspect_ratios || [])
const imageSizeOptions = computed(() => selectedModelCapability.value?.image_sizes || [])
const resolutionOptions = computed(() => selectedModelCapability.value?.resolutions || [])
const qualityOptions = computed(() => (selectedModelCapability.value?.qualities || []).map((value) => ({
  value,
  label: t(`imageStudio.quality${value.charAt(0).toUpperCase()}${value.slice(1)}`)
})))
const backgroundOptions = computed(() => (selectedModelCapability.value?.backgrounds || []).map((value) => ({
  value,
  label: value === 'auto' ? t('imageStudio.backgroundAuto') : t('imageStudio.backgroundOpaque')
})))
const outputFormats = computed(() => selectedModelCapability.value?.output_formats || [])
const maxImageCount = computed(() => provider.value === 'gemini' ? 1 : selectedModelCapability.value?.max_images || 1)
const maxInputImageCount = computed(() => selectedModelCapability.value?.max_input_images || 0)
const hasSourceImages = computed(() => sourceImages.value.length > 0)
const selectedSizeOption = computed(() => sizeOptions.value.find((option) => option.value === form.size))
const sizeValidation = computed(() => validateGPTImage2Size(form.size))
const customWidthBounds = computed(() => gptImage2DimensionBounds(Number(customSize.height)))
const customHeightBounds = computed(() => gptImage2DimensionBounds(Number(customSize.width)))
const selectedSizeShape = computed(() => {
  if (provider.value !== 'openai') return shapeForAspectRatio(form.aspectRatio)
  if (sizeMode.value === 'auto') return 'auto'
  if (sizeMode.value !== 'custom') return selectedSizeOption.value?.shape || 'square'
  const width = Number(customSize.width)
  const height = Number(customSize.height)
  if (width === height) return 'square'
  return width > height ? 'landscape' : 'portrait'
})
const selectedSizeLabel = computed(() => form.size === 'auto' ? t('imageStudio.sizeAuto') : form.size)
const selectedQualityLabel = computed(() => qualityOptions.value.find((option) => option.value === form.quality)?.label || form.quality)
const sizeErrorMessage = computed(() => {
  if (provider.value !== 'openai' || sizeValidation.value.valid) return ''
  const messages = {
    format: 'imageStudio.sizeErrorFormat',
    multiple: 'imageStudio.sizeErrorMultiple',
    edge: 'imageStudio.sizeErrorEdge',
    ratio: 'imageStudio.sizeErrorRatio',
    pixelsMin: 'imageStudio.sizeErrorPixelsMin',
    pixelsMax: 'imageStudio.sizeErrorPixelsMax'
  } as const
  return t(messages[sizeValidation.value.error || 'format'])
})
const canGenerate = computed(() => {
  if (loadingCapabilities.value || readingSourceImages.value || !capabilities.value || !selectedModelCapability.value) return false
  if (form.apiKeyId <= 0 || !form.prompt.trim()) return false
  if (provider.value === 'openai') {
    return sizeValidation.value.valid
      && qualityOptions.value.some((option) => option.value === form.quality)
      && backgroundOptions.value.some((option) => option.value === form.background)
      && outputFormats.value.includes(form.outputFormat)
  }
  if (!aspectRatioOptions.value.includes(form.aspectRatio)) return false
  if (provider.value === 'gemini') {
    return imageSizeOptions.value.length === 0 || imageSizeOptions.value.includes(form.imageSize)
  }
  if (provider.value === 'grok' && hasSourceImages.value) {
    return sourceImages.value.length === 1 || aspectRatioOptions.value.includes(form.aspectRatio)
  }
  return resolutionOptions.value.includes(form.resolution)
    && (form.quality === 'low' || form.quality === 'medium')
    && qualityOptions.value.some((option) => option.value === form.quality)
    && form.count >= 1 && form.count <= maxImageCount.value
})
const selectionSummary = computed(() => {
  if (provider.value === 'openai') return [selectedSizeLabel.value, selectedQualityLabel.value, form.count].join(' · ')
  if (provider.value === 'gemini') return [form.aspectRatio, form.imageSize].filter(Boolean).join(' · ')
  if (provider.value === 'grok' && hasSourceImages.value) {
    return [
      t('imageStudio.sourceImagesAttached', { count: sourceImages.value.length }),
      ...(sourceImages.value.length > 1 ? [form.aspectRatio] : [])
    ].join(' · ')
  }
  if (provider.value === 'grok') return [form.aspectRatio, form.resolution.toUpperCase(), selectedQualityLabel.value, form.count].filter(Boolean).join(' · ')
  return t('imageStudio.selectApiKey')
})
const selectedPrice = computed(() => {
  const prices = pricing.value?.prices || []
  const modelPrices = prices.filter((price) => !price.model || price.model === form.model)
  if (provider.value === 'openai') {
    return modelPrices.find((price) => price.size === form.size)
      || modelPrices.find((price) => price.billing_tier === imageBillingTierForSize(form.size))
  }
  if (provider.value === 'gemini') {
    return modelPrices.find((price) => price.image_size === form.imageSize || price.size === form.imageSize)
      || modelPrices.find((price) => price.billing_tier.toLowerCase() === form.imageSize.toLowerCase())
      || modelPrices[0]
  }
  return modelPrices.find((price) => price.resolution === form.resolution || price.size === form.resolution)
    || modelPrices.find((price) => price.billing_tier.toLowerCase() === form.resolution.toLowerCase())
    || modelPrices[0]
})
const selectedUnitPrice = computed(() => selectedPrice.value?.unit_price ?? null)
const pricedImageCount = computed(() => provider.value === 'gemini' || (provider.value === 'grok' && hasSourceImages.value) ? 1 : form.count)
const activeGenerationCount = computed(() => generationJobs.value.filter((job) => job.status === 'generating').length)
const formattedEstimate = computed(() => {
  if (loadingPricing.value) return '...'
  if (selectedUnitPrice.value != null) return formatPrice(selectedUnitPrice.value * pricedImageCount.value)
  if (pricing.value?.pricing_kind === 'usage_based') return t('imageStudio.usageBasedPricing')
  return '—'
})
const previewSource = computed(() => {
  if (previewSourceImage.value) return sourceImageURL(previewSourceImage.value)
  if (!previewItem.value) return ''
  return 'imageSrc' in previewItem.value ? previewItem.value.imageSrc : previewItem.value.thumbnailSrc || ''
})
const previewAlt = computed(() => previewSourceImage.value?.name || previewItem.value?.prompt || '')
const previewCaption = computed(() => previewSourceImage.value?.name || previewItem.value?.prompt || '')
const previewDetail = computed(() => {
  if (previewSourceImage.value) {
    return `${previewSourceImage.value.mime_type.replace('image/', '').toUpperCase()} · ${formatFileSize(previewSourceImage.value.size)}`
  }
  if (!previewItem.value) return ''
  const summary = galleryItemSummary(previewItem.value)
  return 'recordVersion' in previewItem.value
    ? [t('imageStudio.archiveThumbnail'), summary].filter(Boolean).join(' · ')
    : summary
})

function storageKey(): string {
  return `image_studio_settings:${userId.value}`
}

function isSizePreset(value: string): boolean {
  return (GPT_IMAGE_2_SIZE_PRESETS as readonly string[]).includes(value)
}

function applySizeMode() {
  sizeWasAdjusted.value = false
  if (sizeMode.value !== 'custom') {
    form.size = sizeMode.value
    return
  }
  const current = validateGPTImage2Size(form.size)
  if (current.valid && !current.auto && current.width && current.height) {
    customSize.width = current.width
    customSize.height = current.height
    lastValidCustomSize.width = current.width
    lastValidCustomSize.height = current.height
  }
  syncCustomSize()
}

function syncCustomSize() {
  if (sizeMode.value !== 'custom') return
  sizeWasAdjusted.value = false
  form.size = `${customSize.width}x${customSize.height}`
}

function normalizeCustomDimension(axis: 'width' | 'height') {
  const otherAxis = axis === 'width' ? 'height' : 'width'
  const rawValue = Number(customSize[axis])
  const rawOther = Number(customSize[otherAxis])
  const normalizedOther = snapGPTImage2Edge(rawOther) || lastValidCustomSize[otherAxis]
  const normalizedValue = normalizeGPTImage2Dimension(rawValue, normalizedOther)

  if (normalizedValue === null) {
    customSize.width = lastValidCustomSize.width
    customSize.height = lastValidCustomSize.height
  } else {
    customSize[axis] = normalizedValue
    customSize[otherAxis] = normalizedOther
  }
  form.size = `${customSize.width}x${customSize.height}`

  const validation = validateGPTImage2Size(form.size)
  if (!validation.valid || !validation.width || !validation.height) {
    customSize.width = lastValidCustomSize.width
    customSize.height = lastValidCustomSize.height
    form.size = `${customSize.width}x${customSize.height}`
  } else {
    lastValidCustomSize.width = validation.width
    lastValidCustomSize.height = validation.height
  }
  sizeWasAdjusted.value = rawValue !== Number(customSize[axis]) || rawOther !== Number(customSize[otherAxis])
}

function swapCustomSize() {
  const width = customSize.width
  customSize.width = customSize.height
  customSize.height = width
  syncCustomSize()
  const validation = validateGPTImage2Size(form.size)
  if (validation.valid && validation.width && validation.height) {
    lastValidCustomSize.width = validation.width
    lastValidCustomSize.height = validation.height
  }
}

function restoreSize(size: string, savedMode?: string) {
  const normalized = size.trim().toLowerCase()
  const validation = validateGPTImage2Size(normalized)
  if (!validation.valid) return

  form.size = normalized
  if (savedMode === 'custom' && !validation.auto && validation.width && validation.height) {
    sizeMode.value = 'custom'
    customSize.width = validation.width
    customSize.height = validation.height
    lastValidCustomSize.width = validation.width
    lastValidCustomSize.height = validation.height
    return
  }
  sizeMode.value = normalized === 'auto' || isSizePreset(normalized) ? normalized : 'custom'
  if (sizeMode.value === 'custom' && validation.width && validation.height) {
    customSize.width = validation.width
    customSize.height = validation.height
    lastValidCustomSize.width = validation.width
    lastValidCustomSize.height = validation.height
  }
}

function imageKeyAllowed(key: ApiKey): boolean {
  const platform = key.group?.platform
  return key.status === 'active'
    && (platform === 'openai' || platform === 'gemini' || platform === 'grok')
    && key.group?.allow_image_generation === true
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
    restoreSettings()
    if (!keys.some((key) => key.id === form.apiKeyId)) form.apiKeyId = keys[0]?.id || 0
    if (form.apiKeyId) await loadCapabilities()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('imageStudio.loadKeysFailed')))
  } finally {
    loadingKeys.value = false
  }
}

function restoreSettings() {
  try {
    const saved = JSON.parse(localStorage.getItem(storageKey()) || '{}') as Partial<typeof form> & { sizeMode?: string }
    if (typeof saved.apiKeyId === 'number') form.apiKeyId = saved.apiKeyId
    if (typeof saved.model === 'string') form.model = saved.model
    if (typeof saved.size === 'string') restoreSize(saved.size, saved.sizeMode)
    if (typeof saved.aspectRatio === 'string') form.aspectRatio = saved.aspectRatio
    if (typeof saved.imageSize === 'string') form.imageSize = saved.imageSize
    if (typeof saved.resolution === 'string') form.resolution = saved.resolution
    if (typeof saved.quality === 'string') form.quality = saved.quality as ImageQuality
    if (typeof saved.background === 'string') form.background = saved.background as ImageBackground
    if (typeof saved.outputFormat === 'string') form.outputFormat = saved.outputFormat as ImageOutputFormat
    if (typeof saved.count === 'number') form.count = Math.max(1, saved.count)
  } catch {
    localStorage.removeItem(storageKey())
  }
}

function persistSettings() {
  if (!userId.value) return
  try {
    localStorage.setItem(storageKey(), JSON.stringify({ ...form, prompt: undefined, sizeMode: sizeMode.value }))
  } catch {
    // Settings persistence is optional.
  }
}

async function loadCapabilities() {
  capabilitiesController?.abort()
  capabilities.value = null
  capabilitiesError.value = ''
  pricingController?.abort()
  pricing.value = null
  pricingError.value = ''
  if (!form.apiKeyId) return
  const requestedKeyId = form.apiKeyId
  const requestController = new AbortController()
  capabilitiesController = requestController
  loadingCapabilities.value = true
  try {
    const result = await getImageStudioCapabilities(requestedKeyId, requestController.signal)
    if (capabilitiesController !== requestController || form.apiKeyId !== requestedKeyId) return
    capabilities.value = result
    if (!result.models.some((model) => model.id === form.model)) form.model = result.default_model
    applyModelCapabilities()
    await loadPricing()
  } catch (error: any) {
    if (capabilitiesController === requestController && error?.code !== 'ERR_CANCELED' && error?.name !== 'AbortError') {
      capabilitiesError.value = extractApiErrorMessage(error, t('imageStudio.loadCapabilitiesFailed'))
    }
  } finally {
    if (capabilitiesController === requestController) {
      loadingCapabilities.value = false
      capabilitiesController = null
    }
  }
}

function applyModelCapabilities() {
  const model = selectedModelCapability.value
  if (!model) return
  form.aspectRatio = validOrFirst(form.aspectRatio, model.aspect_ratios)
  form.imageSize = validOrFirst(form.imageSize, model.image_sizes)
  form.resolution = validOrFirst(form.resolution, model.resolutions)
  form.quality = validOrFirst(form.quality, model.qualities) as ImageQuality
  form.background = validOrFirst(form.background, model.backgrounds) as ImageBackground
  form.outputFormat = validOrFirst(form.outputFormat, model.output_formats) as ImageOutputFormat
  form.count = provider.value === 'gemini' ? 1 : Math.min(Math.max(1, form.count), model.max_images)
  reconcileSourceImages(model.max_input_images)
}

function validOrFirst<T extends string>(current: string, values: T[]): T | '' {
  return values.includes(current as T) ? current as T : values[0] || ''
}

function reconcileSourceImages(limit: number) {
  const normalizedLimit = Math.max(0, limit || 0)
  if (sourceImages.value.length <= normalizedLimit) return
  const removed = sourceImages.value.slice(normalizedLimit)
  sourceImages.value = sourceImages.value.slice(0, normalizedLimit)
  if (previewSourceImage.value && removed.some((image) => image.id === previewSourceImage.value?.id)) {
    previewSourceImage.value = null
  }
  appStore.showWarning(t('imageStudio.sourceImagesTrimmed', { max: normalizedLimit }))
}

function isSourceImageMime(value: string): value is ImageStudioSourceImage['mime_type'] {
  return (sourceImageMimeTypes as readonly string[]).includes(value)
}

function readFileAsBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error || new Error('Failed to read source image'))
    reader.onload = () => {
      const result = String(reader.result || '')
      const separator = result.indexOf(',')
      if (separator < 0 || !result.slice(separator + 1)) {
        reject(new Error('Source image did not contain image data'))
        return
      }
      resolve(result.slice(separator + 1))
    }
    reader.readAsDataURL(file)
  })
}

async function handleSourceImageFiles(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files || [])
  input.value = ''
  if (readingSourceImages.value || files.length === 0) return

  const limit = maxInputImageCount.value
  const slots = Math.max(0, limit - sourceImages.value.length)
  if (slots === 0) {
    appStore.showError(t('imageStudio.sourceImageLimitReached', { max: limit }))
    return
  }
  readingSourceImages.value = true
  try {
    const next: SourceImageDraft[] = []
    let reachedLimit = false
    let totalBytes = sourceImages.value.reduce((sum, image) => sum + image.size, 0)
    for (const file of files) {
      if (!isSourceImageMime(file.type)) {
        appStore.showError(t('imageStudio.sourceImageFormatUnsupported', { name: file.name }))
        continue
      }
      if (file.size > maxSourceImageBytes) {
        appStore.showError(t('imageStudio.sourceImageTooLarge', { name: file.name }))
        continue
      }
      if (next.length >= slots) {
        reachedLimit = true
        continue
      }
      if (totalBytes + file.size > maxSourceImagesTotalBytes) {
        appStore.showError(t('imageStudio.sourceImagesTotalTooLarge'))
        continue
      }
      try {
        const data = await readFileAsBase64(file)
        next.push({
          id: globalThis.crypto?.randomUUID?.() || `source-image-${Date.now()}-${Math.random()}`,
          name: file.name,
          size: file.size,
          mime_type: file.type,
          data
        })
        totalBytes += file.size
      } catch {
        appStore.showError(t('imageStudio.sourceImageReadFailed', { name: file.name }))
      }
    }
    if (reachedLimit) appStore.showError(t('imageStudio.sourceImageLimitReached', { max: limit }))

    const remaining = Math.max(0, maxInputImageCount.value - sourceImages.value.length)
    sourceImages.value = [...sourceImages.value, ...next.slice(0, remaining)]
  } finally {
    readingSourceImages.value = false
  }
}

function sourceImageURL(image: ImageStudioSourceImage): string {
  return sanitizeImageStudioSource(`data:${image.mime_type};base64,${image.data}`)
}

function removeSourceImage(id: string) {
  sourceImages.value = sourceImages.value.filter((image) => image.id !== id)
  if (previewSourceImage.value?.id === id) previewSourceImage.value = null
}

function openSourceImagePreview(image: SourceImageDraft) {
  previewItem.value = null
  previewSourceImage.value = image
}

function openGalleryPreview(item: ImageStudioGalleryItem | ImageStudioArchiveItem) {
  if (!('imageSrc' in item) && !item.thumbnailSrc) return
  previewSourceImage.value = null
  previewItem.value = item
}

function closePreview() {
  previewItem.value = null
  previewSourceImage.value = null
}

function formatFileSize(bytes: number): string {
  return bytes >= 1024 * 1024
    ? `${(bytes / (1024 * 1024)).toFixed(1)} MB`
    : `${Math.max(1, Math.round(bytes / 1024))} KB`
}

async function loadPricing() {
  pricingController?.abort()
  pricing.value = null
  pricingError.value = ''
  if (!form.apiKeyId || !form.model) return
  const requestController = new AbortController()
  pricingController = requestController
  loadingPricing.value = true
  try {
    const result = await getImageStudioPricing(form.apiKeyId, form.model, requestController.signal)
    if (pricingController === requestController) pricing.value = result
  } catch (error: any) {
    if (pricingController === requestController && error?.code !== 'ERR_CANCELED' && error?.name !== 'AbortError') {
      pricingError.value = extractApiErrorMessage(error, t('imageStudio.loadPricingFailed'))
    }
  } finally {
    if (pricingController === requestController) {
      loadingPricing.value = false
      pricingController = null
    }
  }
}

async function loadGallery() {
  loadingGallery.value = true
  try {
    const restored = await listImageStudioGallery(userId.value)
    if (!viewUnmounted) archives.value = mergeGalleryItems(archives.value, restored)
  } catch (error) {
    if (!viewUnmounted) appStore.showError(extractApiErrorMessage(error, t('imageStudio.galleryLoadFailed')))
  } finally {
    if (!viewUnmounted) loadingGallery.value = false
  }
}

function mergeGalleryItems<T extends { id: string; createdAt: number; archivedAt?: number }>(current: T[], restored: T[]): T[] {
  const items = new Map(restored.map((item) => [item.id, item]))
  for (const item of current) items.set(item.id, item)
  return [...items.values()].sort((a, b) => (b.archivedAt || b.createdAt) - (a.archivedAt || a.createdAt))
}

function syncGenerationTimer() {
  if (activeGenerationCount.value > 0 && generationTimer === null) {
    generationNow.value = Date.now()
    generationTimer = window.setInterval(() => {
      generationNow.value = Date.now()
    }, 1000)
    return
  }
  if (activeGenerationCount.value === 0) stopGenerationTimer()
}

function stopGenerationTimer() {
  if (generationTimer !== null) window.clearInterval(generationTimer)
  generationTimer = null
}

function elapsedForJob(job: ImageGenerationJob): number {
  return Math.max(0, Math.floor((generationNow.value - job.startedAt) / 1000))
}

function resultSource(result: ImageStudioImage, snapshot: ImageGenerationSnapshot): string {
  if (result.b64_json) {
    const fallbackMime = snapshot.provider === 'openai' ? `image/${snapshot.outputFormat}` : 'image/jpeg'
    const mime = ['image/png', 'image/jpeg', 'image/webp'].includes(result.mime_type || '') ? result.mime_type! : fallbackMime
    return sanitizeImageStudioSource(`data:${mime};base64,${result.b64_json}`)
  }
  return sanitizeImageStudioSource(result.url)
}

function resultFormat(result: ImageStudioImage, snapshot: ImageGenerationSnapshot): ImageOutputFormat {
  if (result.mime_type === 'image/jpeg') return 'jpeg'
  if (result.mime_type === 'image/webp') return 'webp'
  if (result.mime_type === 'image/png') return 'png'
  return snapshot.provider === 'openai' ? snapshot.outputFormat : 'jpeg'
}

function generationPayload(settings: ImageGenerationSettings): ImageStudioGenerationRequest {
  const sourceImages = settings.sourceImages.map((image) => ({ ...image }))
  const base = {
    api_key_id: settings.apiKeyId,
    prompt: settings.prompt,
    model: settings.model,
    ...(sourceImages.length ? { source_images: sourceImages } : {})
  }
  if (settings.provider === 'gemini') {
    return {
      ...base,
      aspect_ratio: settings.aspectRatio,
      ...(settings.imageSize ? { image_size: settings.imageSize } : {})
    }
  }
  if (settings.provider === 'grok') {
    if (sourceImages.length) {
      return {
        ...base,
        source_images: sourceImages,
        ...(sourceImages.length > 1 && settings.aspectRatio !== 'auto'
          ? { aspect_ratio: settings.aspectRatio }
          : {})
      }
    }
    return {
      ...base,
      aspect_ratio: settings.aspectRatio,
      resolution: settings.resolution,
      quality: settings.quality as 'low' | 'medium',
      n: settings.count
    }
  }
  return {
    ...base,
    size: settings.size,
    quality: settings.quality,
    background: settings.background,
    output_format: settings.outputFormat,
    n: settings.count
  }
}

function captureGenerationSnapshot(): ImageGenerationSnapshot | null {
  if (!provider.value) return null
  const sourceImageSnapshot = sourceImages.value.map(({ mime_type, data }) => ({ mime_type, data }))
  const isGrokEdit = provider.value === 'grok' && sourceImageSnapshot.length > 0
  const effectiveAspectRatio = isGrokEdit && sourceImageSnapshot.length === 1 ? 'auto' : form.aspectRatio
  const settings: ImageGenerationSettings = {
    userId: userId.value,
    apiKeyId: form.apiKeyId,
    provider: provider.value,
    model: form.model,
    prompt: form.prompt.trim(),
    size: form.size,
    aspectRatio: effectiveAspectRatio,
    imageSize: form.imageSize,
    resolution: form.resolution,
    quality: form.quality,
    background: form.background,
    outputFormat: form.outputFormat,
    count: provider.value === 'gemini' || isGrokEdit ? 1 : form.count,
    expectedCount: provider.value === 'gemini' || isGrokEdit ? 1 : form.count,
    aspectToken: provider.value === 'openai' ? form.size : effectiveAspectRatio,
    sourceImages: sourceImageSnapshot
  }
  return Object.freeze({
    ...settings,
    payload: Object.freeze(generationPayload(settings))
  })
}

function createGenerationJob(snapshot: ImageGenerationSnapshot): ImageGenerationJob {
  return {
    id: `image-generation-${Date.now()}-${generationSequence++}`,
    status: 'generating',
    error: '',
    startedAt: Date.now(),
    snapshot
  }
}

function replaceGenerationJob(job: ImageGenerationJob) {
  generationJobs.value = generationJobs.value.map((candidate) => candidate.id === job.id ? job : candidate)
}

function removeGenerationJob(jobId: string) {
  generationJobs.value = generationJobs.value.filter((job) => job.id !== jobId)
}

function galleryItemsForResults(
  results: ImageStudioImage[],
  snapshot: ImageGenerationSnapshot
): ImageStudioGalleryItem[] {
  const createdAt = Date.now()
  return results.map((result, index): ImageStudioGalleryItem | null => {
    const imageSrc = resultSource(result, snapshot)
    if (!imageSrc) return null
    const isGrokEdit = snapshot.provider === 'grok' && snapshot.sourceImages.length > 0
    const size = snapshot.provider === 'openai' ? snapshot.size : snapshot.aspectRatio
    return {
      id: globalThis.crypto?.randomUUID?.() || `${createdAt}-${index}-${Math.random()}`,
      userId: snapshot.userId,
      createdAt,
      prompt: snapshot.prompt,
      revisedPrompt: result.revised_prompt,
      apiKeyId: snapshot.apiKeyId,
      provider: snapshot.provider,
      model: snapshot.model,
      size,
      ...(snapshot.provider !== 'openai' ? { aspectRatio: snapshot.aspectRatio } : {}),
      ...(snapshot.provider === 'gemini' && snapshot.imageSize ? { imageSize: snapshot.imageSize } : {}),
      ...(snapshot.provider === 'grok' && !isGrokEdit ? { resolution: snapshot.resolution } : {}),
      ...(snapshot.provider !== 'gemini' && !isGrokEdit ? { quality: snapshot.quality } : {}),
      ...(snapshot.provider === 'openai' ? { background: snapshot.background } : {}),
      outputFormat: resultFormat(result, snapshot),
      count: snapshot.count,
      resultIndex: index,
      sourceImageCount: snapshot.sourceImages.length,
      ...(snapshot.sourceImages.length ? { isEdit: true } : {}),
      imageSrc
    }
  }).filter((item): item is ImageStudioGalleryItem => item !== null)
}

async function executeGeneration(jobId: string) {
  const job = generationJobs.value.find((candidate) => candidate.id === jobId)
  if (!job || generationControllers.has(jobId)) return
  const controller = new AbortController()
  generationControllers.set(jobId, controller)
  syncGenerationTimer()
  try {
    const response = await generateImage(job.snapshot.payload, controller.signal)
    if (generationControllers.get(jobId) !== controller || !generationJobs.value.some((candidate) => candidate.id === jobId)) return
    const items = galleryItemsForResults(response.data, job.snapshot)
    if (items.length === 0) throw new Error(response.data.length ? t('imageStudio.invalidImageReturned') : t('imageStudio.noImagesReturned'))
    gallery.value = [...items, ...gallery.value]
    removeGenerationJob(jobId)
    syncGenerationTimer()
    appStore.showSuccess(t('imageStudio.generated', { count: items.length }))
  } catch (error: any) {
    if (generationControllers.get(jobId) !== controller || !generationJobs.value.some((candidate) => candidate.id === jobId)) return
    const canceled = controller.signal.aborted || error?.code === 'ERR_CANCELED' || error?.name === 'AbortError'
    if (canceled) {
      removeGenerationJob(jobId)
    } else {
      const message = extractApiErrorMessage(error, t('imageStudio.generationFailed'))
      replaceGenerationJob({ ...job, status: 'error', error: message })
      appStore.showError(message)
    }
  } finally {
    if (generationControllers.get(jobId) === controller) generationControllers.delete(jobId)
    syncGenerationTimer()
  }
}

function generate() {
  if (!canGenerate.value) return
  const snapshot = captureGenerationSnapshot()
  if (!snapshot) return
  const job = createGenerationJob(snapshot)
  generationJobs.value = [job, ...generationJobs.value]
  void executeGeneration(job.id)
}

function cancelGeneration(jobId: string) {
  const controller = generationControllers.get(jobId)
  generationControllers.delete(jobId)
  removeGenerationJob(jobId)
  controller?.abort()
  syncGenerationTimer()
}

function retryGeneration(jobId: string) {
  const job = generationJobs.value.find((candidate) => candidate.id === jobId)
  if (!job || job.status !== 'error') return
  const retry = { ...job, status: 'generating' as const, error: '', startedAt: Date.now() }
  replaceGenerationJob(retry)
  void executeGeneration(jobId)
}

function dismissGeneration(jobId: string) {
  if (generationJobs.value.some((job) => job.id === jobId && job.status === 'error')) {
    removeGenerationJob(jobId)
  }
}

function reuseItem(item: ImageStudioGalleryItem | ImageStudioArchiveItem) {
  if (imageKeys.value.some((key) => key.id === item.apiKeyId)) form.apiKeyId = item.apiKeyId
  form.prompt = item.prompt
  form.model = item.model
  if (item.provider === 'openai') restoreSize(item.size)
  if (item.aspectRatio) form.aspectRatio = item.aspectRatio
  if (item.imageSize) form.imageSize = item.imageSize
  if (item.resolution) form.resolution = item.resolution
  if (item.quality) form.quality = item.quality as ImageQuality
  if (item.background) form.background = item.background as ImageBackground
  if (item.outputFormat) form.outputFormat = item.outputFormat as ImageOutputFormat
  if (item.count) form.count = item.count
  sourceImages.value = []
  previewSourceImage.value = null
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

function isArchived(id: string): boolean {
  return deletingArchiveIds.value.has(id) || archives.value.some((item) => item.id === id)
}

function isArchiving(id: string): boolean {
  return archivingIds.value.has(id)
}

function setArchiving(id: string, active: boolean) {
  const next = new Set(archivingIds.value)
  if (active) next.add(id)
  else next.delete(id)
  archivingIds.value = next
}

function setDeletingArchive(id: string, active: boolean) {
  const next = new Set(deletingArchiveIds.value)
  if (active) next.add(id)
  else next.delete(id)
  deletingArchiveIds.value = next
}

async function archiveItem(item: ImageStudioGalleryItem) {
  if (loadingGallery.value || clearingArchive.value || isArchived(item.id) || isArchiving(item.id)) return
  setArchiving(item.id, true)
  try {
    const thumbnailSrc = await createImageStudioThumbnail(item.imageSrc)
    const archive = createImageStudioArchiveItem(item, thumbnailSrc)
    await saveImageStudioGalleryItem(archive)
    if (viewUnmounted) return
    archives.value = mergeGalleryItems(archives.value, [archive])
    appStore.showSuccess(t('imageStudio.archiveSaved'))
  } catch (error) {
    if (viewUnmounted) return
    appStore.showError(isImageStudioArchiveLimitError(error)
      ? t('imageStudio.archiveLimitReached')
      : extractApiErrorMessage(error, t('imageStudio.archiveSaveFailed')))
  } finally {
    if (!viewUnmounted) setArchiving(item.id, false)
  }
}

function removeResult(item: ImageStudioGalleryItem) {
  gallery.value = gallery.value.filter((entry) => entry.id !== item.id)
  if (previewItem.value?.id === item.id && 'imageSrc' in previewItem.value) previewItem.value = null
}

function clearResults() {
  if (!window.confirm(t('imageStudio.clearConfirm'))) return
  gallery.value = []
  if (previewItem.value && 'imageSrc' in previewItem.value) previewItem.value = null
}

async function deleteArchive(item: ImageStudioArchiveItem) {
  if (loadingGallery.value || clearingArchive.value || deletingArchiveIds.value.has(item.id)) return
  setDeletingArchive(item.id, true)
  archives.value = archives.value.filter((entry) => entry.id !== item.id)
  if (previewItem.value?.id === item.id && 'recordVersion' in previewItem.value) previewItem.value = null
  try {
    await deleteImageStudioGalleryItem(userId.value, item.id)
  } catch (error) {
    if (!viewUnmounted) {
      archives.value = mergeGalleryItems(archives.value, [item])
      appStore.showError(extractApiErrorMessage(error, t('imageStudio.archiveDeleteFailed')))
    }
  } finally {
    if (!viewUnmounted) setDeletingArchive(item.id, false)
  }
}

async function clearArchive() {
  if (loadingGallery.value || clearingArchive.value || archivingIds.value.size > 0 || deletingArchiveIds.value.size > 0) return
  if (!window.confirm(t('imageStudio.clearArchiveConfirm'))) return
  const previous = archives.value
  clearingArchive.value = true
  archives.value = []
  if (previewItem.value && 'recordVersion' in previewItem.value) previewItem.value = null
  try {
    await clearImageStudioGallery(userId.value)
  } catch (error) {
    if (!viewUnmounted) {
      archives.value = mergeGalleryItems(archives.value, previous)
      appStore.showError(extractApiErrorMessage(error, t('imageStudio.archiveClearFailed')))
    }
  } finally {
    if (!viewUnmounted) clearingArchive.value = false
  }
}

function shapeForAspectRatio(value: string): 'auto' | 'square' | 'landscape' | 'portrait' {
  if (!value || value === 'auto') return 'auto'
  const [width, height] = value.split(':').map(Number)
  if (!width || !height || width === height) return 'square'
  return width > height ? 'landscape' : 'portrait'
}

function aspectRatioStyle(value: string): Record<string, string> {
  if (!value || value === 'auto') return { aspectRatio: '1 / 1' }
  const separator = value.includes(':') ? ':' : 'x'
  const [width, height] = value.split(separator).map(Number)
  return width > 0 && height > 0 ? { aspectRatio: `${width} / ${height}` } : { aspectRatio: '1 / 1' }
}

function segmentColumnClass(count: number): string {
  if (count <= 2) return 'two-columns'
  if (count === 3) return 'three-columns'
  return 'four-columns'
}

function galleryItemSummary(item: ImageStudioGalleryItem | ImageStudioArchiveItem): string {
  return [
    item.model,
    item.provider === 'openai' ? item.size : item.aspectRatio,
    item.imageSize || item.resolution,
    item.quality,
    item.outputFormat?.toUpperCase()
  ].filter(Boolean).join(' · ')
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
  return new Intl.DateTimeFormat(locale.value || undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }).format(value)
}

function downloadName(item: ImageStudioGalleryItem): string {
  return `image-studio-${new Date(item.createdAt).toISOString().replace(/[:.]/g, '-')}.${item.outputFormat}`
}

watch(() => [
  form.apiKeyId,
  form.model,
  form.size,
  sizeMode.value,
  form.aspectRatio,
  form.imageSize,
  form.resolution,
  form.quality,
  form.background,
  form.outputFormat,
  form.count
], persistSettings)
watch(() => form.apiKeyId, () => {
  if (!loadingKeys.value) void loadCapabilities()
})
watch(() => form.model, (model, previousModel) => {
  if (!capabilities.value || !model || model === previousModel) return
  applyModelCapabilities()
  void loadPricing()
})

onMounted(async () => {
  await Promise.all([loadKeys(), loadGallery()])
})

onBeforeUnmount(() => {
  viewUnmounted = true
  for (const controller of generationControllers.values()) controller.abort()
  generationControllers.clear()
  capabilitiesController?.abort()
  pricingController?.abort()
  stopGenerationTimer()
})
</script>

<style scoped>
.image-studio-shell { display: flex; min-height: calc(100vh - 8.5rem); flex-direction: column; gap: 1rem; }
.studio-status { display: flex; min-height: 2.75rem; align-items: center; gap: .75rem; border-bottom: 1px solid rgb(229 231 235); color: rgb(75 85 99); font-size: .8125rem; }
.studio-model-mark { display: flex; align-items: center; gap: .5rem; color: rgb(17 24 39); font-weight: 700; }
.studio-status-divider { height: 1rem; width: 1px; background: rgb(209 213 219); }
.studio-status-key { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.studio-price-summary { margin-left: auto; display: flex; align-items: baseline; gap: .5rem; white-space: nowrap; }
.studio-price-summary strong { color: rgb(5 150 105); font-size: .9375rem; }
.studio-grid { display: grid; min-width: 0; min-height: 36rem; width: 100%; flex: 1 0 auto; grid-template-columns: minmax(19rem, 21rem) minmax(0, 1fr); align-items: stretch; gap: 1.25rem; }
.studio-controls, .studio-workspace { min-width: 0; overflow: hidden; border: 1px solid rgb(229 231 235); border-radius: 8px; background: rgb(255 255 255 / .92); box-shadow: 0 1px 2px rgb(0 0 0 / .04); }
.studio-controls { display: flex; flex-direction: column; }
.studio-control-scroll { display: flex; flex-direction: column; gap: 1.125rem; padding: 1.25rem; }
.studio-workspace { display: flex; flex-direction: column; }
.control-group { display: flex; flex-direction: column; gap: .5rem; }
.control-label { color: rgb(55 65 81); font-size: .75rem; font-weight: 700; }
.capabilities-error { border: 1px solid rgb(254 202 202); border-radius: 6px; background: rgb(254 242 242); padding: .625rem .75rem; color: rgb(185 28 28); font-size: .75rem; line-height: 1.45; }
.studio-select, .studio-prompt { width: 100%; border: 1px solid rgb(209 213 219); border-radius: 6px; background: white; color: rgb(17 24 39); font-size: .875rem; outline: none; transition: border-color .15s, box-shadow .15s; }
.studio-select { height: 2.625rem; padding: 0 .75rem; }
.prompt-workspace { min-width: 0; overflow: hidden; }
.prompt-header { display: flex; min-height: 3rem; align-items: center; justify-content: space-between; padding: 0 1.25rem; }
.prompt-header > span { color: rgb(156 163 175); font-size: .6875rem; font-variant-numeric: tabular-nums; }
.studio-prompt { display: block; min-height: 16rem; max-height: 40rem; width: calc(100% - 2.5rem); margin: 0 1.25rem; resize: vertical; padding: .875rem; line-height: 1.65; }
.studio-select:focus, .studio-prompt:focus { border-color: rgb(13 148 136); box-shadow: 0 0 0 3px rgb(20 184 166 / .12); }
.source-images-control { display: flex; margin: .875rem 1.25rem 0; flex-direction: column; gap: .625rem; border: 1px solid rgb(229 231 235); border-radius: 6px; background: rgb(249 250 251 / .72); padding: .75rem; }
.source-images-header { display: flex; min-width: 0; align-items: center; justify-content: space-between; gap: .75rem; }
.source-images-label { display: flex; min-width: 0; align-items: baseline; gap: .5rem; }
.source-images-label small, .source-images-hint { color: rgb(107 114 128); font-size: .6875rem; }
.source-image-upload-button { display: inline-flex; min-height: 2.25rem; flex: 0 0 auto; cursor: pointer; align-items: center; justify-content: center; gap: .4rem; border: 1px solid rgb(209 213 219); border-radius: 6px; background: white; padding: .375rem .75rem; color: rgb(55 65 81); font-size: .75rem; font-weight: 650; }
.source-image-upload-button:hover:not(.disabled) { border-color: rgb(153 246 228); color: rgb(13 148 136); }
.source-image-upload-button.disabled { cursor: not-allowed; opacity: .45; }
.source-image-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(4.75rem, 5.5rem)); gap: .625rem; }
.source-image-item { position: relative; min-width: 0; }
.source-image-preview-button { display: block; width: 100%; aspect-ratio: 1; overflow: hidden; border: 1px solid rgb(209 213 219); border-radius: 6px; background: white; }
.source-image-preview-button:hover { border-color: rgb(94 234 212); }
.source-image-preview-button img { height: 100%; width: 100%; object-fit: cover; }
.source-image-remove-button { position: absolute; right: -.3rem; top: -.3rem; display: inline-flex; height: 1.35rem; width: 1.35rem; align-items: center; justify-content: center; border: 1px solid rgb(229 231 235); border-radius: 9999px; background: white; color: rgb(75 85 99); box-shadow: 0 1px 2px rgb(0 0 0 / .1); }
.source-image-remove-button:hover { border-color: rgb(254 202 202); color: rgb(220 38 38); }
.source-image-item > span { display: block; overflow: hidden; margin-top: .25rem; color: rgb(107 114 128); font-size: .625rem; text-align: center; text-overflow: ellipsis; white-space: nowrap; }
.size-select-shell { position: relative; }
.size-select { padding-left: 2.75rem; }
.size-shape { position: absolute; left: .875rem; top: 50%; z-index: 1; flex: 0 0 auto; border: 1.5px solid currentColor; border-radius: 2px; color: rgb(107 114 128); pointer-events: none; transform: translateY(-50%); }
.size-shape.square { height: 1.25rem; width: 1.25rem; }
.size-shape.landscape { height: .9rem; width: 1.4rem; }
.size-shape.portrait { height: 1.4rem; width: .9rem; }
.size-shape.auto { height: 1.15rem; width: 1.15rem; border-style: dashed; }
.custom-size-editor { display: grid; grid-template-columns: minmax(0, 1fr) 2.25rem minmax(0, 1fr); align-items: end; gap: .5rem; }
.custom-size-field { display: flex; min-width: 0; flex-direction: column; gap: .35rem; color: rgb(107 114 128); font-size: .6875rem; font-weight: 600; }
.dimension-input-shell { position: relative; display: block; }
.dimension-input-shell input { height: 2.5rem; width: 100%; border: 1px solid rgb(209 213 219); border-radius: 6px; background: white; padding: 0 2rem 0 .625rem; color: rgb(17 24 39); font-size: .8125rem; font-variant-numeric: tabular-nums; outline: none; }
.dimension-input-shell input:focus { border-color: rgb(13 148 136); box-shadow: 0 0 0 3px rgb(20 184 166 / .12); }
.dimension-input-shell input[aria-invalid="true"] { border-color: rgb(239 68 68); }
.dimension-input-shell small { position: absolute; right: .625rem; top: 50%; color: rgb(156 163 175); font-size: .625rem; pointer-events: none; transform: translateY(-50%); }
.swap-size-button { display: inline-flex; height: 2.5rem; width: 2.25rem; align-items: center; justify-content: center; border: 1px solid rgb(209 213 219); border-radius: 6px; color: rgb(75 85 99); }
.swap-size-button:hover:not(:disabled) { border-color: rgb(94 234 212); color: rgb(13 148 136); }
.swap-size-button:disabled { opacity: .4; }
.size-feedback { display: flex; align-items: flex-start; gap: .35rem; font-size: .6875rem; line-height: 1.45; }
.size-feedback.error { color: rgb(220 38 38); }
.size-feedback.experimental { color: rgb(180 83 9); }
.size-feedback.adjusted { color: rgb(5 150 105); }
.segmented-control { display: grid; overflow: hidden; border: 1px solid rgb(209 213 219); border-radius: 6px; background: rgb(249 250 251); }
.segmented-control.two-columns { grid-template-columns: repeat(2, minmax(0, 1fr)); }
.segmented-control.three-columns { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.segmented-control.four-columns { grid-template-columns: repeat(4, minmax(0, 1fr)); }
.segmented-control button { min-height: 2.375rem; border-right: 1px solid rgb(209 213 219); padding: .375rem .5rem; color: rgb(75 85 99); font-size: .75rem; font-weight: 600; }
.segmented-control button:last-child { border-right: 0; }
.segmented-control button.active { background: white; color: rgb(15 118 110); box-shadow: inset 0 -2px 0 rgb(13 148 136); }
.segmented-control button:disabled { cursor: not-allowed; opacity: .4; }
.segmented-control.compact button { padding-inline: .25rem; }
.stepper { display: grid; height: 2.5rem; grid-template-columns: 2.25rem 1fr 2.25rem; overflow: hidden; border: 1px solid rgb(209 213 219); border-radius: 6px; }
.stepper button { color: rgb(75 85 99); font-size: 1.125rem; }
.stepper button:hover:not(:disabled) { background: rgb(243 244 246); color: rgb(15 118 110); }
.stepper button:disabled { opacity: .35; }
.stepper output { display: flex; align-items: center; justify-content: center; border-inline: 1px solid rgb(229 231 235); color: rgb(17 24 39); font-size: .875rem; font-weight: 700; }
.studio-price-area { display: flex; min-height: 3.75rem; margin-top: auto; align-items: center; justify-content: space-between; gap: .75rem; border-top: 1px solid rgb(229 231 235); background: rgb(249 250 251 / .72); padding: .75rem 1.25rem; }
.studio-price-area > strong { flex: 0 0 auto; color: rgb(5 150 105); font-size: .875rem; }
.price-detail { min-width: 0; color: rgb(107 114 128); font-size: .6875rem; }
.prompt-submit-area { display: flex; min-height: 4.5rem; align-items: center; justify-content: space-between; gap: 1rem; padding: .75rem 1.25rem; }
.prompt-request-summary { display: flex; min-width: 0; flex-direction: column; gap: .3rem; }
.prompt-selection { min-width: 0; overflow: hidden; color: rgb(107 114 128); font-size: .6875rem; text-overflow: ellipsis; white-space: nowrap; }
.active-generation-count { display: inline-flex; min-width: 0; align-items: center; gap: .4rem; color: rgb(13 148 136); font-size: .6875rem; font-weight: 600; }
.generate-button { display: flex; height: 2.875rem; min-width: 9.5rem; flex: 0 0 auto; align-items: center; justify-content: center; gap: .5rem; border-radius: 6px; padding: 0 1.25rem; color: white; font-size: .875rem; font-weight: 700; transition: background-color .15s, opacity .15s; }
.generate-button { background: rgb(13 148 136); }
.generate-button:hover:not(:disabled) { background: rgb(15 118 110); }
.generate-button:disabled { cursor: not-allowed; opacity: .45; }
.studio-gallery { display: flex; min-width: 0; min-height: 0; flex: 1; flex-direction: column; border-top: 1px solid rgb(229 231 235); padding: 1rem 1.25rem 1.25rem; }
.gallery-header { display: flex; min-height: 3.25rem; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.gallery-header h2 { color: rgb(17 24 39); font-size: .9375rem; font-weight: 750; }
.gallery-header p { margin-top: .125rem; color: rgb(107 114 128); font-size: .75rem; }
.gallery-header-actions { display: flex; align-items: center; gap: .5rem; }
.gallery-mode-switch { display: grid; height: 2.25rem; grid-template-columns: repeat(2, minmax(5.5rem, 1fr)); overflow: hidden; border: 1px solid rgb(229 231 235); border-radius: 6px; background: rgb(249 250 251); }
.gallery-mode-switch button { display: inline-flex; min-width: 0; align-items: center; justify-content: center; gap: .35rem; border-right: 1px solid rgb(229 231 235); padding: 0 .625rem; color: rgb(107 114 128); font-size: .6875rem; font-weight: 650; white-space: nowrap; }
.gallery-mode-switch button:last-child { border-right: 0; }
.gallery-mode-switch button.active { background: white; color: rgb(15 118 110); box-shadow: inset 0 -2px 0 rgb(13 148 136); }
.gallery-mode-switch span { color: rgb(156 163 175); font-size: .625rem; font-variant-numeric: tabular-nums; }
.toolbar-icon-button, .gallery-actions button, .gallery-actions a, .preview-close { display: inline-flex; height: 2.25rem; width: 2.25rem; flex: 0 0 auto; align-items: center; justify-content: center; border: 1px solid rgb(229 231 235); border-radius: 6px; background: white; color: rgb(75 85 99); }
.toolbar-icon-button:hover, .gallery-actions button:hover, .gallery-actions a:hover { border-color: rgb(153 246 228); color: rgb(13 148 136); }
.toolbar-icon-button:disabled { cursor: not-allowed; opacity: .45; }
.gallery-empty { display: flex; min-height: 12rem; flex: 1; align-items: center; justify-content: center; flex-direction: column; padding: 1.5rem; text-align: center; }
.gallery-empty .empty-icon { display: flex; height: 3.25rem; width: 3.25rem; align-items: center; justify-content: center; border: 1px solid rgb(209 250 229); border-radius: 8px; background: rgb(236 253 245); color: rgb(5 150 105); }
.gallery-empty h3 { margin-top: 1rem; color: rgb(31 41 55); font-size: .9375rem; font-weight: 700; }
.gallery-empty p { max-width: 27rem; margin: .4rem 0 1.25rem; color: rgb(107 114 128); font-size: .8125rem; line-height: 1.55; }
.gallery-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(15rem, 1fr)); align-items: start; gap: 1rem; }
.gallery-item { overflow: hidden; border: 1px solid rgb(229 231 235); border-radius: 8px; background: white; box-shadow: 0 1px 2px rgb(0 0 0 / .04); }
.gallery-image-button { position: relative; display: block; width: 100%; overflow: hidden; background-color: rgb(243 244 246); }
.gallery-image-button img { height: 100%; width: 100%; object-fit: cover; transition: transform .2s ease; }
.gallery-image-button:hover img { transform: scale(1.015); }
.archive-badge { position: absolute; bottom: .5rem; left: .5rem; border: 1px solid rgb(255 255 255 / .7); border-radius: 4px; background: rgb(17 24 39 / .76); padding: .2rem .4rem; color: white; font-size: .625rem; font-weight: 650; }
.archive-thumbnail-unavailable { display: flex; width: 100%; min-height: 10rem; align-items: center; justify-content: center; flex-direction: column; gap: .5rem; background: rgb(243 244 246); color: rgb(107 114 128); }
.archive-thumbnail-unavailable span { font-size: .6875rem; }
.gallery-item-meta { display: flex; min-height: 4.25rem; align-items: center; justify-content: space-between; gap: .75rem; border-top: 1px solid rgb(243 244 246); padding: .625rem .75rem; }
.gallery-item-meta p { overflow: hidden; color: rgb(55 65 81); font-size: .75rem; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.gallery-item-meta span { display: block; margin-top: .2rem; color: rgb(156 163 175); font-size: .625rem; }
.gallery-actions { display: flex; flex: 0 0 auto; gap: .25rem; }
.gallery-actions button, .gallery-actions a { height: 1.875rem; width: 1.875rem; border-color: transparent; background: transparent; }
.gallery-actions button:disabled { cursor: default; opacity: .55; }
.gallery-actions button.archived { color: rgb(5 150 105); }
.generation-placeholder { display: flex; min-height: 14rem; align-items: center; justify-content: center; flex-direction: column; gap: .5rem; border: 1px dashed rgb(167 243 208); border-radius: 8px; background: rgb(249 250 251); color: rgb(13 148 136); }
.generation-placeholder strong { color: rgb(55 65 81); font-size: .8125rem; }
.generation-placeholder small { max-width: 80%; overflow: hidden; color: rgb(107 114 128); font-size: .6875rem; text-align: center; text-overflow: ellipsis; }
.generation-placeholder .placeholder-prompt { display: -webkit-box; line-height: 1.45; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
.loading-ring { height: 1.5rem; width: 1.5rem; border: 2px solid rgb(209 250 229); border-top-color: rgb(13 148 136); border-radius: 9999px; animation: studio-spin .8s linear infinite; }
.loading-ring.tiny { height: .75rem; width: .75rem; border-width: 1.5px; }
.placeholder-actions { display: flex; align-items: center; justify-content: center; gap: .4rem; margin-top: .25rem; }
.retry-button, .placeholder-icon-button { display: inline-flex; min-height: 2rem; align-items: center; justify-content: center; border: 1px solid rgb(209 213 219); border-radius: 6px; background: white; color: rgb(55 65 81); font-size: .75rem; }
.retry-button { gap: .35rem; padding: .35rem .75rem; }
.placeholder-icon-button { width: 2rem; padding: 0; }
.retry-button:hover, .placeholder-icon-button:hover { border-color: rgb(153 246 228); color: rgb(13 148 136); }
.preview-backdrop { position: fixed; inset: 0; z-index: 80; display: flex; align-items: center; justify-content: center; background: rgb(0 0 0 / .78); padding: 1rem; }
.preview-dialog { position: relative; display: flex; max-height: calc(100vh - 2rem); max-width: min(72rem, calc(100vw - 2rem)); flex-direction: column; overflow: hidden; border-radius: 8px; background: rgb(17 24 39); box-shadow: 0 24px 60px rgb(0 0 0 / .35); }
.preview-dialog > img { min-height: 0; max-height: calc(100vh - 8rem); max-width: 100%; object-fit: contain; }
.preview-close { position: absolute; right: .75rem; top: .75rem; z-index: 1; border-color: rgb(255 255 255 / .25); background: rgb(17 24 39 / .72); color: white; }
.preview-caption { display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding: .875rem 1rem; color: white; }
.preview-caption p { min-width: 0; overflow: hidden; font-size: .8125rem; text-overflow: ellipsis; white-space: nowrap; }
.preview-caption span { flex: 0 0 auto; color: rgb(156 163 175); font-size: .6875rem; }
@keyframes studio-spin { to { transform: rotate(360deg); } }

:global(.dark .studio-status) { border-color: rgb(51 65 85); color: rgb(148 163 184); }
:global(.dark .studio-model-mark), :global(.dark .gallery-header h2) { color: rgb(241 245 249); }
:global(.dark .studio-status-divider) { background: rgb(71 85 105); }
:global(.dark .studio-controls), :global(.dark .studio-workspace), :global(.dark .gallery-item) { border-color: rgb(51 65 85); background: rgb(15 23 42 / .92); }
:global(.dark .control-label), :global(.dark .gallery-item-meta p), :global(.dark .generation-placeholder strong) { color: rgb(203 213 225); }
:global(.dark .studio-select), :global(.dark .studio-prompt) { border-color: rgb(71 85 105); background: rgb(30 41 59); color: rgb(241 245 249); }
:global(.dark .source-images-control) { border-color: rgb(51 65 85); background: rgb(15 23 42 / .5); }
:global(.dark .source-images-label small), :global(.dark .source-images-hint), :global(.dark .source-image-item > span) { color: rgb(148 163 184); }
:global(.dark .source-image-upload-button), :global(.dark .source-image-preview-button), :global(.dark .source-image-remove-button) { border-color: rgb(71 85 105); background: rgb(30 41 59); color: rgb(203 213 225); }
:global(.dark .dimension-input-shell input) { border-color: rgb(71 85 105); background: rgb(30 41 59); color: rgb(241 245 249); }
:global(.dark .custom-size-field) { color: rgb(148 163 184); }
:global(.dark .swap-size-button) { border-color: rgb(71 85 105); color: rgb(148 163 184); }
:global(.dark .swap-size-button:hover:not(:disabled)) { border-color: rgb(45 212 191); color: rgb(94 234 212); }
:global(.dark .size-feedback.error) { color: rgb(248 113 113); }
:global(.dark .size-feedback.experimental) { color: rgb(251 191 36); }
:global(.dark .size-feedback.adjusted) { color: rgb(52 211 153); }
:global(.dark .capabilities-error) { border-color: rgb(127 29 29); background: rgb(69 10 10 / .35); color: rgb(252 165 165); }
:global(.dark .size-shape) { color: rgb(148 163 184); }
:global(.dark .segmented-control) { border-color: rgb(71 85 105); background: rgb(15 23 42); }
:global(.dark .segmented-control button) { border-color: rgb(71 85 105); color: rgb(148 163 184); }
:global(.dark .segmented-control button.active) { background: rgb(30 41 59); color: rgb(94 234 212); }
:global(.dark .stepper) { border-color: rgb(71 85 105); }
:global(.dark .stepper button:hover:not(:disabled)) { background: rgb(30 41 59); color: rgb(94 234 212); }
:global(.dark .stepper output) { border-color: rgb(51 65 85); color: rgb(241 245 249); }
:global(.dark .studio-price-area), :global(.dark .studio-gallery), :global(.dark .gallery-item-meta) { border-color: rgb(51 65 85); }
:global(.dark .studio-price-area) { background: rgb(15 23 42 / .5); }
:global(.dark .prompt-header > span), :global(.dark .price-detail), :global(.dark .prompt-selection) { color: rgb(148 163 184); }
:global(.dark .active-generation-count) { color: rgb(94 234 212); }
:global(.dark .toolbar-icon-button) { border-color: rgb(51 65 85); background: rgb(30 41 59); color: rgb(148 163 184); }
:global(.dark .gallery-mode-switch) { border-color: rgb(51 65 85); background: rgb(15 23 42); }
:global(.dark .gallery-mode-switch button) { border-color: rgb(51 65 85); color: rgb(148 163 184); }
:global(.dark .gallery-mode-switch button.active) { background: rgb(30 41 59); color: rgb(94 234 212); }
:global(.dark .retry-button), :global(.dark .placeholder-icon-button) { border-color: rgb(71 85 105); background: rgb(30 41 59); color: rgb(203 213 225); }
:global(.dark .gallery-empty h3) { color: rgb(226 232 240); }
:global(.dark .gallery-empty .empty-icon) { border-color: rgb(6 78 59); background: rgb(6 78 59 / .35); color: rgb(94 234 212); }
:global(.dark .gallery-image-button), :global(.dark .generation-placeholder) { background: rgb(15 23 42); }
:global(.dark .archive-thumbnail-unavailable) { background: rgb(15 23 42); color: rgb(148 163 184); }

@media (max-width: 900px) {
  .image-studio-shell { min-height: auto; }
  .studio-grid { min-height: 0; flex: 0 0 auto; grid-template-columns: 1fr; }
  .studio-control-scroll { overflow: visible; }
  .studio-controls { max-height: none; }
  .studio-gallery { min-height: 22rem; }
}
@media (max-width: 520px) {
  .studio-status-key, .studio-status-divider { display: none; }
  .studio-price-summary span { display: none; }
  .gallery-grid { grid-template-columns: 1fr; }
  .gallery-header { align-items: stretch; flex-direction: column; }
  .gallery-header-actions { justify-content: space-between; }
  .gallery-mode-switch { min-width: 0; flex: 1; }
  .preview-caption { align-items: flex-start; flex-direction: column; }
  .prompt-header { padding-inline: 1rem; }
  .studio-prompt { min-height: 12.5rem; width: calc(100% - 2rem); margin-inline: 1rem; }
  .source-images-control { margin-inline: 1rem; }
  .source-images-header { align-items: flex-start; flex-direction: column; }
  .prompt-submit-area { align-items: stretch; flex-direction: column; gap: .625rem; padding: .75rem 1rem 1rem; }
  .generate-button { width: 100%; }
}
@media (prefers-reduced-motion: reduce) {
  .loading-ring { animation-duration: 1.8s; }
  .gallery-image-button img { transition: none; }
}
</style>
