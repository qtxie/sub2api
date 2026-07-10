<template>
  <AppLayout>
    <div class="chat-shell" :class="{ 'sidebar-collapsed': sidebarCollapsed }">
      <aside class="chat-history" :class="{ collapsed: sidebarCollapsed }">
        <div class="history-header">
          <div class="history-brand" :title="t('chat.title')">
            <span class="history-brand-mark">S</span>
            <span v-if="!sidebarCollapsed" class="history-brand-text">{{ t('chat.title') }}</span>
          </div>
          <button
            type="button"
            class="icon-button history-collapse"
            :title="sidebarCollapsed ? t('chat.expandSidebar') : t('chat.collapseSidebar')"
            @click="toggleSidebar"
          >
            <Icon :name="sidebarCollapsed ? 'chevronRight' : 'chevronLeft'" size="sm" />
          </button>
        </div>

        <div class="history-primary">
          <button
            class="history-command"
            type="button"
            :title="t('chat.newChat')"
            @click="startNewChat"
          >
            <Icon name="edit" size="sm" />
            <span v-if="!sidebarCollapsed">{{ t('chat.newChat') }}</span>
          </button>
        </div>

        <div v-if="!sidebarCollapsed" class="history-list">
          <div class="history-section-title">{{ t('chat.recentChats') }}</div>
          <button
            v-for="conversation in conversations"
            :key="conversation.id"
            type="button"
            class="history-item"
            :class="{ 'history-item-active': activeConversation?.id === conversation.id }"
            @click="selectConversation(conversation.id)"
          >
            <Icon name="chatBubble" size="sm" class="history-icon" />
            <span v-if="renamingId !== conversation.id" class="history-title">{{ conversation.title }}</span>
            <input
              v-else
              v-model="renameTitle"
              class="history-rename"
              @click.stop
              @keydown.enter.prevent="saveRename(conversation)"
              @keydown.esc.prevent="cancelRename"
              @blur="saveRename(conversation)"
            />
            <span class="history-actions" @click.stop>
              <button
                type="button"
                class="icon-button"
                :title="t('chat.rename')"
                @click="beginRename(conversation)"
              >
                <Icon name="edit" size="xs" />
              </button>
              <button
                type="button"
                class="icon-button danger"
                :title="t('chat.delete')"
                @click="deleteTarget = conversation"
              >
                <Icon name="trash" size="xs" />
              </button>
            </span>
          </button>

          <div v-if="!historyLoading && conversations.length === 0" class="history-empty">
            {{ t('chat.noHistory') }}
          </div>
          <div v-if="historyLoading" class="history-empty">
            {{ t('common.loading') }}
          </div>
        </div>

        <div class="history-footer">
          <button
            type="button"
            class="history-command"
            :class="{ active: systemPrompt.trim() }"
            :title="t('chat.chatSettings')"
            @click="settingsOpen = true"
          >
            <Icon name="cog" size="sm" />
            <span v-if="!sidebarCollapsed">{{ t('chat.chatSettings') }}</span>
          </button>
        </div>
      </aside>

      <section class="chat-main">
        <header class="chat-toolbar">
          <div class="toolbar-field">
            <label>{{ t('chat.apiKey') }}</label>
            <select v-model.number="selectedApiKeyId" class="toolbar-select" :disabled="sending || activeApiKeys.length === 0">
              <option :value="null" disabled>{{ t('chat.selectApiKey') }}</option>
              <option v-for="key in activeApiKeys" :key="key.id" :value="key.id">
                {{ key.name }}
              </option>
            </select>
          </div>
          <div class="toolbar-field">
            <label>{{ t('chat.model') }}</label>
            <select
              v-model="selectedModel"
              class="toolbar-select"
              :disabled="sending || !selectedApiKeyId || loadingModels || modelSelectOptions.length === 0"
            >
              <option value="" disabled>{{ modelSelectPlaceholder }}</option>
              <option v-for="model in modelSelectOptions" :key="model" :value="model">
                {{ model }}
              </option>
            </select>
          </div>
          <div class="toolbar-field">
            <label>{{ t('chat.reasoningEffort') }}</label>
            <select v-model="selectedReasoningEffort" class="toolbar-select" :disabled="sending">
              <option v-for="option in availableReasoningEffortOptions" :key="option.value" :value="option.value">
                {{ t(option.label) }}
              </option>
            </select>
          </div>
          <button
            type="button"
            class="btn btn-secondary toolbar-refresh"
            :title="t('common.refresh')"
            :disabled="loadingInitial"
            @click="loadInitialData"
          >
            <Icon name="refresh" size="sm" :class="{ 'animate-spin': loadingInitial }" />
          </button>
        </header>

        <div ref="messagesEl" class="messages-pane">
          <div v-if="activeApiKeys.length === 0" class="empty-state">
            <Icon name="key" size="xl" />
            <h2>{{ t('chat.noApiKeys') }}</h2>
            <router-link to="/keys" class="btn btn-primary">
              <Icon name="plus" size="sm" />
              <span>{{ t('keys.createKey') }}</span>
            </router-link>
          </div>
          <div v-else-if="messages.length === 0 && !loadingConversation" class="empty-state">
            <Icon name="chat" size="xl" />
            <h2>{{ t('chat.emptyTitle') }}</h2>
          </div>
          <div v-else class="message-stack">
            <article
              v-for="message in messages"
              :key="message.id"
              class="message-row"
              :class="message.role === 'user' ? 'message-row-user' : 'message-row-assistant'"
            >
              <template v-if="message.role === 'user'">
                <div class="message-bubble message-user">
                  <div class="message-content" v-html="renderMarkdown(message.content)"></div>
                  <div v-if="messageTransientAttachments(message).length > 0" class="message-attachments">
                    <div
                      v-for="attachment in messageTransientAttachments(message)"
                      :key="attachment.id"
                      class="message-attachment"
                    >
                      <img
                        v-if="attachment.type === 'image' && attachment.preview_url"
                        class="message-attachment-image"
                        :src="attachment.preview_url"
                        :alt="attachment.name"
                      />
                      <Icon v-else name="document" size="sm" />
                      <span>{{ attachment.name }}</span>
                    </div>
                  </div>
                  <div v-if="message.error_message" class="message-error">{{ message.error_message }}</div>
                  <div class="message-tools message-tools-user">
                    <button type="button" class="text-button" @click="copyMessage(message.content)">
                      <Icon name="copy" size="xs" />
                      <span>{{ t('chat.copy') }}</span>
                    </button>
                  </div>
                </div>
              </template>
              <div v-else class="assistant-message">
                <div v-if="message.status === 'error' || message.status === 'cancelled'" class="message-meta assistant-meta">
                  <span v-if="message.status === 'error'" class="message-status error">{{ t('chat.failed') }}</span>
                  <span v-else class="message-status">{{ t('chat.stopped') }}</span>
                </div>
                <div
                  v-if="isWaitingAssistant(message)"
                  class="typing-indicator"
                  :aria-label="t('chat.waiting')"
                >
                  <span></span>
                  <span></span>
                  <span></span>
                </div>
                <div v-if="message.content" class="message-content assistant-content" v-html="renderMarkdown(message.content)"></div>
                <div
                  v-if="isStreamingAssistant(message)"
                  class="typing-indicator typing-indicator-inline"
                  :aria-label="t('chat.waiting')"
                >
                  <span></span>
                  <span></span>
                  <span></span>
                </div>
                <div v-if="message.error_message" class="message-error">{{ message.error_message }}</div>
                <div class="message-tools assistant-tools">
                  <button type="button" class="text-button" @click="copyMessage(message.content)">
                    <Icon name="copy" size="xs" />
                    <span>{{ t('chat.copy') }}</span>
                  </button>
                  <button
                    v-if="isLastAssistant(message)"
                    type="button"
                    class="text-button"
                    :disabled="sending"
                    @click="regenerateLast"
                  >
                    <Icon name="refresh" size="xs" />
                    <span>{{ t('chat.regenerate') }}</span>
                  </button>
                </div>
              </div>
            </article>
          </div>
        </div>

        <form class="composer" @submit.prevent="sendMessage">
          <div v-if="pendingAttachments.length > 0" class="attachment-strip">
            <div v-for="attachment in pendingAttachments" :key="attachment.id" class="attachment-chip">
              <img
                v-if="attachment.type === 'image' && attachment.preview_url"
                class="attachment-thumb"
                :src="attachment.preview_url"
                :alt="attachment.name"
              />
              <Icon v-else name="document" size="sm" />
              <span class="attachment-name">{{ attachment.name }}</span>
              <button
                type="button"
                class="icon-button"
                :title="t('chat.removeAttachment')"
                @click="removeAttachment(attachment.id)"
              >
                <Icon name="x" size="xs" />
              </button>
            </div>
          </div>
          <div class="composer-controls">
            <div class="attachment-control" @click.stop>
              <button
                type="button"
                class="composer-attach-button"
                :title="t('chat.addAttachment')"
                :disabled="sending || activeApiKeys.length === 0 || attachmentLoading"
                @click="attachmentMenuOpen = !attachmentMenuOpen"
              >
                <Icon name="plus" size="md" />
              </button>
              <div v-if="attachmentMenuOpen" class="attachment-menu">
                <button type="button" class="attachment-menu-item" @click="triggerImagePicker">
                  <Icon name="upload" size="sm" />
                  <span>{{ t('chat.uploadImage') }}</span>
                </button>
                <button type="button" class="attachment-menu-item" @click="triggerFilePicker">
                  <Icon name="document" size="sm" />
                  <span>{{ t('chat.uploadFile') }}</span>
                </button>
              </div>
            </div>
            <textarea
              ref="composerInput"
              v-model="draft"
              class="composer-input"
              rows="1"
              :placeholder="t('chat.composerPlaceholder')"
              :disabled="sending || activeApiKeys.length === 0"
              @input="resizeComposerInput"
              @paste="resizeComposerInput"
              @keydown.enter.exact.prevent="sendMessage"
            ></textarea>
            <button
              v-if="!sending"
              type="submit"
              class="composer-button"
              :title="t('chat.send')"
              :disabled="!canSend"
            >
              <Icon name="arrowUp" size="md" />
            </button>
            <button v-else type="button" class="composer-button stop" :title="t('chat.stop')" @click="stopStreaming">
              <Icon name="x" size="md" />
            </button>
          </div>
          <input
            ref="imageInput"
            class="sr-only"
            type="file"
            accept="image/*"
            multiple
            @change="handleImageSelection"
          />
          <input
            ref="fileInput"
            class="sr-only"
            type="file"
            accept=".txt,.md,.markdown,.csv,.json,.jsonl,.xml,.yaml,.yml,.log,.html,.htm,.css,.js,.jsx,.ts,.tsx,.go,.py,.java,.c,.cc,.cpp,.h,.hpp,.rs,.sql,.sh,.ps1,text/*,application/json,application/xml,application/yaml,application/x-yaml,application/javascript"
            multiple
            @change="handleFileSelection"
          />
        </form>
      </section>
    </div>

    <ConfirmDialog
      :show="deleteTarget !== null"
      :title="t('chat.deleteTitle')"
      :message="t('chat.deleteMessage', { title: deleteTarget?.title || '' })"
      :confirm-text="t('chat.delete')"
      danger
      @confirm="confirmDelete"
      @cancel="deleteTarget = null"
    />

    <ConfirmDialog
      :show="clearLocalAttachmentsConfirmOpen"
      :title="t('chat.clearLocalAttachments')"
      :message="t('chat.clearLocalAttachmentsConfirm')"
      :confirm-text="t('chat.clearLocalAttachments')"
      danger
      @confirm="clearLocalAttachments"
      @cancel="clearLocalAttachmentsConfirmOpen = false"
    />

    <div v-if="settingsOpen" class="settings-backdrop" @click.self="settingsOpen = false">
      <section class="settings-dialog" role="dialog" aria-modal="true" :aria-label="t('chat.chatSettings')">
        <header class="settings-header">
          <h2>{{ t('chat.chatSettings') }}</h2>
          <button type="button" class="icon-button" :title="t('common.close')" @click="settingsOpen = false">
            <Icon name="x" size="sm" />
          </button>
        </header>
        <label class="settings-field">
          <span>{{ t('chat.systemPrompt') }}</span>
          <textarea
            v-model="systemPrompt"
            class="system-input"
            rows="6"
            maxlength="8192"
            :placeholder="t('chat.systemPromptPlaceholder')"
            :disabled="sending"
          ></textarea>
        </label>
        <div class="settings-field">
          <span>{{ t('chat.exportAll') }}</span>
          <button
            type="button"
            class="btn btn-secondary settings-export"
            :disabled="exportingChats"
            @click="exportAllChats"
          >
            <Icon name="download" size="sm" :class="{ 'animate-spin': exportingChats }" />
            <span>{{ t('chat.exportAll') }}</span>
          </button>
        </div>
        <div class="settings-field">
          <span>{{ t('chat.localAttachments') }}</span>
          <button
            type="button"
            class="btn btn-secondary settings-export"
            :disabled="clearingLocalAttachments"
            @click="clearLocalAttachmentsConfirmOpen = true"
          >
            <Icon name="trash" size="sm" :class="{ 'animate-spin': clearingLocalAttachments }" />
            <span>{{ t('chat.clearLocalAttachments') }}</span>
          </button>
        </div>
        <footer class="settings-actions">
          <button type="button" class="btn btn-secondary" @click="systemPrompt = ''">
            {{ t('chat.clearSystemPrompt') }}
          </button>
          <button type="button" class="btn btn-primary" @click="settingsOpen = false">
            {{ t('chat.done') }}
          </button>
        </footer>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import 'katex/dist/katex.min.css'
import AppLayout from '@/components/layout/AppLayout.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import chatAPI, {
  type ChatConversation,
  type ChatMessage,
  type ChatReasoningEffort,
  type ChatStreamAttachment,
} from '@/api/chat'
import keysAPI from '@/api/keys'
import type { ApiKey } from '@/types'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { extractApiErrorMessage } from '@/utils/apiError'
import { renderChatMarkdown } from '@/utils/chatMarkdown'
import {
  chatReasoningEffortOptionsForModel,
  normalizeChatReasoningEffort
} from '@/utils/chatReasoning'
import {
  bindChatAttachmentsToMessage,
  chatAttachmentDraftKey,
  cleanupUserChatAttachments,
  clearUserChatAttachments,
  deleteChatAttachment,
  deleteChatAttachmentsForConversation,
  listChatAttachmentDrafts,
  listChatConversationAttachments,
  reassignChatAttachmentDrafts,
  saveChatAttachmentDraft,
  type StoredChatAttachment,
} from '@/utils/chatAttachmentStore'

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()

const conversations = ref<ChatConversation[]>([])
const activeConversation = ref<ChatConversation | null>(null)
const messages = ref<ChatMessage[]>([])
const apiKeys = ref<ApiKey[]>([])
const modelsByApiKey = ref<Record<number, string[]>>({})
const selectedApiKeyId = ref<number | null>(null)
const selectedModel = ref('')
const selectedReasoningEffort = ref<ChatReasoningEffort>('')
const systemPrompt = ref('')
const draft = ref('')
const historyLoading = ref(false)
const loadingConversation = ref(false)
const loadingInitial = ref(false)
const loadingModelApiKeyId = ref<number | null>(null)
const sending = ref(false)
const exportingChats = ref(false)
const settingsOpen = ref(false)
const sidebarCollapsed = ref(false)
const messagesEl = ref<HTMLElement | null>(null)
const composerInput = ref<HTMLTextAreaElement | null>(null)
const abortController = ref<AbortController | null>(null)
const deleteTarget = ref<ChatConversation | null>(null)
const renamingId = ref<number | null>(null)
const renameTitle = ref('')
const pendingAttachments = ref<PendingChatAttachment[]>([])
const attachmentMenuOpen = ref(false)
const attachmentLoading = ref(false)
const clearingLocalAttachments = ref(false)
const clearLocalAttachmentsConfirmOpen = ref(false)
const imageInput = ref<HTMLInputElement | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)

let tempMessageId = -1
let modelLoadToken = 0
let pendingModelSelection = ''
let assistantDeltaText = ''
let assistantDeltaFrame: number | null = null
let assistantDeltaDrainResolve: (() => void) | null = null
let attachmentLoadToken = 0

type PendingChatAttachment = {
  id: string
  type: 'image' | 'file'
  name: string
  mime_type: string
  size: number
  blob?: Blob
  text?: string
  preview_url?: string
}

const attachmentObjectURLs = new Set<string>()

const maxChatAttachments = 8
const maxChatImageBytes = 10 * 1024 * 1024
const maxChatFileBytes = 256 * 1024

const activeApiKeys = computed(() => apiKeys.value.filter((key) => key.status === 'active'))
const modelOptions = computed(() => {
  const id = selectedApiKeyId.value
  return id ? modelsByApiKey.value[id] || [] : []
})
const modelSelectOptions = computed(() => {
  const models = [...modelOptions.value]
  const current = selectedModel.value.trim()
  if (current && !models.includes(current)) models.unshift(current)
  return models
})
const loadingModels = computed(() => selectedApiKeyId.value !== null && loadingModelApiKeyId.value === selectedApiKeyId.value)
const modelSelectPlaceholder = computed(() => {
  if (loadingModels.value) return t('chat.loadingModels')
  return modelOptions.value.length === 0 ? t('chat.noModels') : t('chat.selectModel')
})
const availableReasoningEffortOptions = computed(() => chatReasoningEffortOptionsForModel(selectedModel.value))
const canSend = computed(() => Boolean(
  (draft.value.trim() || pendingAttachments.value.length > 0) &&
  selectedApiKeyId.value &&
  selectedModel.value.trim() &&
  !sending.value &&
  !attachmentLoading.value
))

watch(selectedApiKeyId, (id) => {
  void refreshModelsForSelectedKey(id)
})

watch(selectedModel, (model) => {
  if (selectedApiKeyId.value && model) {
    localStorage.setItem(`chat_last_model_${selectedApiKeyId.value}`, model)
  }
  coerceSelectedReasoningEffort()
})

watch(selectedReasoningEffort, (effort) => {
  localStorage.setItem('chat_last_reasoning_effort', effort)
})

watch(draft, () => {
  void resizeComposerInput()
})

async function resizeComposerInput() {
  await nextTick()
  const input = composerInput.value
  if (!input) return

  const style = window.getComputedStyle(input)
  const lineHeight = Number.parseFloat(style.lineHeight) || 24
  const paddingTop = Number.parseFloat(style.paddingTop) || 0
  const paddingBottom = Number.parseFloat(style.paddingBottom) || 0
  const borderTop = Number.parseFloat(style.borderTopWidth) || 0
  const borderBottom = Number.parseFloat(style.borderBottomWidth) || 0
  const maxHeight = lineHeight * 6 + paddingTop + paddingBottom + borderTop + borderBottom

  input.style.height = 'auto'
  const nextHeight = Math.min(input.scrollHeight, maxHeight)
  input.style.height = `${nextHeight}px`
  input.style.overflowY = input.scrollHeight > maxHeight ? 'auto' : 'hidden'
}

async function loadInitialData() {
  loadingInitial.value = true
  await Promise.all([loadKeys(), loadConversations()])
  loadingInitial.value = false
}

async function loadKeys() {
  const previousApiKeyId = selectedApiKeyId.value
  try {
    const keyPage = await keysAPI.list(1, 100, { status: 'active', sort_by: 'created_at', sort_order: 'desc' })
    apiKeys.value = keyPage.items
    applyDefaultSelection()
    if (selectedApiKeyId.value && selectedApiKeyId.value === previousApiKeyId) {
      await refreshModelsForSelectedKey(selectedApiKeyId.value, true)
    }
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('chat.loadFailed')))
  }
}

async function loadConversations() {
  historyLoading.value = true
  try {
    const page = await chatAPI.listConversations()
    conversations.value = page.items
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('chat.loadFailed')))
  } finally {
    historyLoading.value = false
  }
}

function applyDefaultSelection() {
  if (activeApiKeys.value.length === 0) {
    selectedApiKeyId.value = null
    selectedModel.value = ''
    return
  }
  const storedKey = Number(localStorage.getItem('chat_last_api_key_id') || 0)
  const defaultKey = activeApiKeys.value.find((key) => key.id === storedKey) || activeApiKeys.value[0]
  selectedApiKeyId.value = defaultKey.id
}

async function refreshModelsForSelectedKey(id: number | null, force = false) {
  const token = ++modelLoadToken
  if (!id) {
    selectedModel.value = ''
    pendingModelSelection = ''
    return
  }

  localStorage.setItem('chat_last_api_key_id', String(id))
  const models = await loadModelsForKey(id, force)
  if (token !== modelLoadToken || selectedApiKeyId.value !== id) return

  const preferredModel = pendingModelSelection || localStorage.getItem(`chat_last_model_${id}`) || ''
  pendingModelSelection = ''
  selectedModel.value = preferredModel && models.includes(preferredModel) ? preferredModel : models[0] || ''
}

async function loadModelsForKey(apiKeyId: number, force = false): Promise<string[]> {
  const cached = modelsByApiKey.value[apiKeyId]
  if (cached && !force) return cached

  loadingModelApiKeyId.value = apiKeyId
  try {
    const response = await chatAPI.listModels(apiKeyId)
    const models = Array.from(new Set((response.models || []).filter(Boolean))).sort((a, b) => a.localeCompare(b))
    modelsByApiKey.value = { ...modelsByApiKey.value, [apiKeyId]: models }
    return models
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('chat.loadModelsFailed')))
    return modelsByApiKey.value[apiKeyId] || []
  } finally {
    if (loadingModelApiKeyId.value === apiKeyId) {
      loadingModelApiKeyId.value = null
    }
  }
}

async function selectConversation(id: number) {
  if (sending.value || activeConversation.value?.id === id) return
  loadingConversation.value = true
  try {
    const conversation = await chatAPI.getConversation(id)
    activeConversation.value = conversation
    pendingModelSelection = conversation.model || ''
    selectedApiKeyId.value = conversation.api_key_id ?? null
    selectedModel.value = conversation.model || ''
    selectedReasoningEffort.value = normalizeReasoningEffort(conversation.reasoning_effort)
    systemPrompt.value = conversation.system_prompt || ''
    await restoreLocalAttachments(conversation.id, conversation.messages || [])
    await scrollToBottom()
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('chat.loadFailed')))
  } finally {
    loadingConversation.value = false
  }
}

async function startNewChat() {
  if (sending.value) return
  activeConversation.value = null
  messages.value = []
  systemPrompt.value = ''
  draft.value = ''
  void resizeComposerInput()
  await restoreLocalAttachments(null, [])
}

function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value
  localStorage.setItem('chat_sidebar_collapsed', sidebarCollapsed.value ? '1' : '0')
}

async function exportAllChats() {
  if (exportingChats.value) return
  exportingChats.value = true
  try {
    const blob = await chatAPI.exportConversations()
    const filename = `sub2api-chat-export-${new Date().toISOString().slice(0, 10)}.json`
    downloadBlob(blob, filename)
    appStore.showSuccess(t('chat.exportSuccess'))
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('chat.exportFailed')))
  } finally {
    exportingChats.value = false
  }
}

function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  URL.revokeObjectURL(url)
}

function triggerImagePicker() {
  attachmentMenuOpen.value = false
  imageInput.value?.click()
}

function triggerFilePicker() {
  attachmentMenuOpen.value = false
  fileInput.value?.click()
}

async function handleImageSelection(event: Event) {
  const input = event.target as HTMLInputElement
  await addFiles(input.files, 'image')
  input.value = ''
}

async function handleFileSelection(event: Event) {
  const input = event.target as HTMLInputElement
  await addFiles(input.files, 'file')
  input.value = ''
}

async function addFiles(fileList: FileList | null, type: 'image' | 'file') {
  if (!fileList || fileList.length === 0) return
  attachmentLoading.value = true
  try {
    for (const file of Array.from(fileList)) {
      if (pendingAttachments.value.length >= maxChatAttachments) {
        appStore.showError(t('chat.tooManyAttachments', { max: maxChatAttachments }))
        break
      }
      try {
        const attachment = type === 'image'
          ? await imageFileToAttachment(file)
          : await textFileToAttachment(file)
        if (attachment) {
          pendingAttachments.value.push(attachment)
          await persistDraftAttachment(attachment).catch(() => {
            appStore.showWarning(t('chat.localAttachmentSaveFailed'))
          })
        }
      } catch {
        appStore.showError(t('chat.readAttachmentFailed'))
      }
    }
  } finally {
    attachmentLoading.value = false
  }
}

async function imageFileToAttachment(file: File): Promise<PendingChatAttachment | null> {
  if (!file.type.startsWith('image/')) {
    appStore.showError(t('chat.unsupportedFile'))
    return null
  }
  if (file.size > maxChatImageBytes) {
    appStore.showError(t('chat.imageTooLarge'))
    return null
  }
  return {
    id: makeAttachmentId(),
    type: 'image',
    name: file.name,
    mime_type: file.type,
    size: file.size,
    blob: file,
    preview_url: createAttachmentPreview(file),
  }
}

async function textFileToAttachment(file: File): Promise<PendingChatAttachment | null> {
  if (!isSupportedTextFile(file)) {
    appStore.showError(t('chat.unsupportedFile'))
    return null
  }
  if (file.size > maxChatFileBytes) {
    appStore.showError(t('chat.fileTooLarge'))
    return null
  }
  const text = (await file.text()).trim()
  if (!text) {
    appStore.showError(t('chat.emptyFile'))
    return null
  }
  return {
    id: makeAttachmentId(),
    type: 'file',
    name: file.name,
    mime_type: file.type || 'text/plain',
    size: file.size,
    text,
  }
}

function isSupportedTextFile(file: File): boolean {
  if (file.type.startsWith('text/')) return true
  if (['application/json', 'application/xml', 'application/yaml', 'application/x-yaml', 'application/javascript'].includes(file.type)) {
    return true
  }
  const lowerName = file.name.toLowerCase()
  return [
    '.txt', '.md', '.markdown', '.csv', '.json', '.jsonl', '.xml', '.yaml', '.yml',
    '.log', '.html', '.htm', '.css', '.js', '.jsx', '.ts', '.tsx', '.go', '.py',
    '.java', '.c', '.cc', '.cpp', '.h', '.hpp', '.rs', '.sql', '.sh', '.ps1',
  ].some((suffix) => lowerName.endsWith(suffix))
}

function readBlobAsDataURL(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      if (typeof reader.result === 'string') {
        resolve(reader.result)
      } else {
        reject(new Error('Invalid file data'))
      }
    }
    reader.onerror = () => reject(reader.error || new Error('Failed to read file'))
    reader.readAsDataURL(blob)
  })
}

async function removeAttachment(id: string) {
  const attachment = pendingAttachments.value.find((item) => item.id === id)
  pendingAttachments.value = pendingAttachments.value.filter((item) => item.id !== id)
  if (attachment) releaseAttachmentPreview(attachment)
  try {
    await deleteChatAttachment(currentUserId(), id)
  } catch {
    appStore.showWarning(t('chat.localAttachmentDeleteFailed'))
  }
}

function clearPendingAttachments(releasePreviews = true) {
  if (releasePreviews) pendingAttachments.value.forEach(releaseAttachmentPreview)
  pendingAttachments.value = []
  attachmentMenuOpen.value = false
}

function makeAttachmentId(): string {
  return `attachment-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

async function toStreamAttachment(attachment: PendingChatAttachment): Promise<ChatStreamAttachment> {
  const dataUrl = attachment.type === 'image' && attachment.blob
    ? await readBlobAsDataURL(attachment.blob)
    : undefined
  return {
    type: attachment.type,
    name: attachment.name,
    mime_type: attachment.mime_type,
    size: attachment.size,
    data_url: dataUrl,
    text: attachment.text,
  }
}

function currentUserId(): number {
  return authStore.user?.id || 0
}

function createAttachmentPreview(blob: Blob): string {
  const url = URL.createObjectURL(blob)
  attachmentObjectURLs.add(url)
  return url
}

function releaseAttachmentPreview(attachment: PendingChatAttachment) {
  const url = attachment.preview_url
  if (!url || !attachmentObjectURLs.has(url)) return
  URL.revokeObjectURL(url)
  attachmentObjectURLs.delete(url)
}

function releaseAllAttachmentPreviews() {
  for (const url of attachmentObjectURLs) URL.revokeObjectURL(url)
  attachmentObjectURLs.clear()
}

function storedAttachmentToPending(record: StoredChatAttachment): PendingChatAttachment {
  return {
    id: record.id,
    type: record.type,
    name: record.name,
    mime_type: record.mimeType,
    size: record.size,
    blob: record.blob,
    text: record.text,
    preview_url: record.type === 'image' && record.blob ? createAttachmentPreview(record.blob) : undefined,
  }
}

function attachmentToStoredDraft(attachment: PendingChatAttachment): StoredChatAttachment {
  const now = Date.now()
  const conversationId = activeConversation.value?.id || 0
  return {
    id: attachment.id,
    userId: currentUserId(),
    conversationId,
    messageId: 0,
    draftKey: chatAttachmentDraftKey(conversationId),
    type: attachment.type,
    name: attachment.name,
    mimeType: attachment.mime_type,
    size: attachment.size,
    blob: attachment.blob,
    text: attachment.text,
    createdAt: now,
    lastAccessedAt: now,
  }
}

async function persistDraftAttachment(attachment: PendingChatAttachment) {
  await saveChatAttachmentDraft(attachmentToStoredDraft(attachment))
}

async function persistDraftAttachments(attachments: PendingChatAttachment[]) {
  await Promise.all(attachments.map(persistDraftAttachment))
}

async function restoreLocalAttachments(conversationId: number | null, serverMessages: ChatMessage[]) {
  const token = ++attachmentLoadToken
  const userId = currentUserId()
  try {
    const [storedMessages, storedDrafts] = await Promise.all([
      conversationId ? listChatConversationAttachments(userId, conversationId) : Promise.resolve([]),
      listChatAttachmentDrafts(userId, chatAttachmentDraftKey(conversationId)),
    ])
    if (token !== attachmentLoadToken) return

    releaseAllAttachmentPreviews()
    const attachmentsByMessage = new Map<number, PendingChatAttachment[]>()
    const attachmentOnlyMessages = new Set<number>()
    for (const record of storedMessages) {
      const list = attachmentsByMessage.get(record.messageId) || []
      list.push(storedAttachmentToPending(record))
      attachmentsByMessage.set(record.messageId, list)
      if (record.attachmentOnly) attachmentOnlyMessages.add(record.messageId)
    }
    messages.value = serverMessages.map((message) => {
      const attachments = attachmentsByMessage.get(message.id) || []
      return attachments.length > 0
        ? withTransientAttachments(message, attachments, attachmentOnlyMessages.has(message.id))
        : message
    })
    pendingAttachments.value = storedDrafts.slice(0, maxChatAttachments).map(storedAttachmentToPending)
  } catch {
    if (token === attachmentLoadToken) {
      releaseAllAttachmentPreviews()
      messages.value = serverMessages
      pendingAttachments.value = []
      appStore.showWarning(t('chat.localAttachmentsLoadFailed'))
    }
  }
}

function withTransientAttachments(message: ChatMessage, attachments: PendingChatAttachment[], attachmentOnly: boolean): ChatMessage {
  if (attachments.length === 0) return message
  return {
    ...message,
    metadata: {
      ...message.metadata,
      transient_attachments: attachments,
      transient_attachment_only: attachmentOnly,
    },
  }
}

function messageTransientAttachments(message: ChatMessage): PendingChatAttachment[] {
  const value = message.metadata?.transient_attachments
  if (!Array.isArray(value)) return []
  return value.filter(isPendingChatAttachment)
}

function isPendingChatAttachment(value: unknown): value is PendingChatAttachment {
  if (!value || typeof value !== 'object') return false
  const attachment = value as Partial<PendingChatAttachment>
  return Boolean(
    attachment.id &&
    (attachment.type === 'image' || attachment.type === 'file') &&
    attachment.name &&
    attachment.mime_type
  )
}

async function ensureConversation(firstMessage: string): Promise<ChatConversation> {
  const title = activeConversation.value?.title || makeTitle(firstMessage)
  const payload = {
    title,
    api_key_id: selectedApiKeyId.value,
    model: selectedModel.value.trim(),
    system_prompt: systemPrompt.value.trim(),
    reasoning_effort: selectedReasoningEffort.value,
  }
  if (!activeConversation.value) {
    const created = await chatAPI.createConversation(payload)
    activeConversation.value = created
    conversations.value = [created, ...conversations.value]
    return created
  }
  const updated = await chatAPI.updateConversation(activeConversation.value.id, payload)
  activeConversation.value = updated
  upsertConversation(updated)
  return updated
}

async function sendMessage() {
  if (!canSend.value) return
  const content = draft.value.trim()
  const attachments = pendingAttachments.value.slice()
  const persistedContent = content || t('chat.attachmentOnlyMessage')
  draft.value = ''
  void resizeComposerInput()
  clearPendingAttachments(false)
  sending.value = true
  abortController.value = new AbortController()

  let assistantTemp: ChatMessage | null = null
  try {
    const conversation = await ensureConversation(content || attachments[0]?.name || persistedContent)
    try {
      await reassignChatAttachmentDrafts(currentUserId(), attachments.map((item) => item.id), conversation.id)
    } catch {
      appStore.showWarning(t('chat.localAttachmentSaveFailed'))
    }
    const userMessage = await chatAPI.appendMessage(conversation.id, { role: 'user', content: persistedContent })
    try {
      await bindChatAttachmentsToMessage(
        currentUserId(),
        attachments.map((item) => item.id),
        conversation.id,
        userMessage.id,
        content === '',
      )
    } catch {
      appStore.showWarning(t('chat.localAttachmentSaveFailed'))
    }
    messages.value.push(withTransientAttachments(userMessage, attachments, content === ''))
    assistantTemp = createTempAssistant()
    messages.value.push(assistantTemp)
    resetAssistantDeltaQueue()
    await scrollToBottom()

    const saved = await chatAPI.streamConversationMessage({
      conversationId: conversation.id,
      attachments: await Promise.all(attachments.map(toStreamAttachment)),
      signal: abortController.value.signal,
      onDelta(delta) {
        if (assistantTemp) queueAssistantDelta(assistantTemp.id, delta)
        void scrollToBottom()
      },
    })
    await drainAssistantDeltas(assistantTemp.id)
    replaceMessage(assistantTemp.id, saved)
    await loadConversations()
  } catch (err) {
    const aborted = abortController.value?.signal.aborted
    if (!assistantTemp && attachments.length > 0) {
      pendingAttachments.value = attachments
    }
    if (assistantTemp) {
      const saved = chatAPI.savedMessageFromChatStreamError(err)
      if (saved) {
        await drainAssistantDeltas(assistantTemp.id)
        replaceMessage(assistantTemp.id, saved)
        await loadConversations()
      } else {
        await drainAssistantDeltas(assistantTemp.id)
        updateMessage(assistantTemp.id, (message) => ({
          ...message,
          status: aborted ? 'cancelled' : 'error',
          content: message.content || (aborted ? t('chat.stopped') : t('chat.failed')),
          error_message: aborted ? '' : extractApiErrorMessage(err, t('chat.failed')),
        }))
      }
    }
    if (!aborted) appStore.showError(extractApiErrorMessage(err, t('chat.failed')))
  } finally {
    sending.value = false
    abortController.value = null
    resetAssistantDeltaQueue()
    await scrollToBottom()
  }
}

function stopStreaming() {
  abortController.value?.abort()
}

function closeAttachmentMenu() {
  attachmentMenuOpen.value = false
}

async function regenerateLast() {
  if (!activeConversation.value || sending.value) return
  const lastAssistantIndex = messages.value.length - 1
  const lastAssistant = messages.value[lastAssistantIndex]
  if (!lastAssistant || lastAssistant.role !== 'assistant') return
  const lastUser = [...messages.value.slice(0, lastAssistantIndex)].reverse().find((message) => message.role === 'user')
  if (!lastAssistant || !lastUser) return
  const transientAttachments = messageTransientAttachments(lastUser)
  try {
    if (lastAssistant.id > 0) await chatAPI.deleteMessage(activeConversation.value.id, lastAssistant.id)
    if (lastUser.id > 0) await chatAPI.deleteMessage(activeConversation.value.id, lastUser.id)
    messages.value = messages.value.filter((message) => message.id !== lastAssistant.id && message.id !== lastUser.id)
    draft.value = lastUser.metadata?.transient_attachment_only ? '' : lastUser.content
    pendingAttachments.value = transientAttachments
    try {
      await persistDraftAttachments(transientAttachments)
    } catch {
      appStore.showWarning(t('chat.localAttachmentSaveFailed'))
    }
    await sendMessage()
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('chat.regenerateFailed')))
  }
}

function createTempAssistant(): ChatMessage {
  return {
    id: tempMessageId--,
    conversation_id: activeConversation.value?.id || 0,
    user_id: 0,
    role: 'assistant',
    content: '',
    status: 'complete',
    error_message: '',
    metadata: {},
    created_at: Math.floor(Date.now() / 1000),
    updated_at: Math.floor(Date.now() / 1000),
  }
}

function replaceMessage(tempId: number, message: ChatMessage) {
  const index = messages.value.findIndex((item) => item.id === tempId)
  if (index >= 0) messages.value[index] = message
}

function updateMessage(id: number, update: (message: ChatMessage) => ChatMessage) {
  const index = messages.value.findIndex((item) => item.id === id)
  if (index >= 0) messages.value[index] = update(messages.value[index])
}

function appendMessageDelta(id: number, delta: string) {
  updateMessage(id, (message) => ({
    ...message,
    content: message.content + delta,
  }))
}

function queueAssistantDelta(id: number, delta: string) {
  assistantDeltaText += delta
  if (assistantDeltaFrame === null) {
    scheduleAssistantDeltaFrame(id)
  }
}

function scheduleAssistantDeltaFrame(id: number) {
  assistantDeltaFrame = window.requestAnimationFrame(() => {
    assistantDeltaFrame = null
    const next = assistantDeltaText.slice(0, 160)
    assistantDeltaText = assistantDeltaText.slice(next.length)
    if (next) appendMessageDelta(id, next)
    if (assistantDeltaText) {
      scheduleAssistantDeltaFrame(id)
      return
    }
    assistantDeltaDrainResolve?.()
    assistantDeltaDrainResolve = null
  })
}

function drainAssistantDeltas(id: number): Promise<void> {
  if (!assistantDeltaText && assistantDeltaFrame === null) return Promise.resolve()
  return new Promise((resolve) => {
    const previousResolve = assistantDeltaDrainResolve
    assistantDeltaDrainResolve = () => {
      previousResolve?.()
      resolve()
    }
    if (assistantDeltaFrame === null && assistantDeltaText) {
      scheduleAssistantDeltaFrame(id)
    }
  })
}

function resetAssistantDeltaQueue() {
  assistantDeltaText = ''
  if (assistantDeltaFrame !== null) {
    window.cancelAnimationFrame(assistantDeltaFrame)
    assistantDeltaFrame = null
  }
  assistantDeltaDrainResolve?.()
  assistantDeltaDrainResolve = null
}

function upsertConversation(conversation: ChatConversation) {
  const index = conversations.value.findIndex((item) => item.id === conversation.id)
  if (index >= 0) {
    conversations.value[index] = conversation
  } else {
    conversations.value.unshift(conversation)
  }
}

function makeTitle(content: string): string {
  const normalized = content.replace(/\s+/g, ' ').trim()
  return normalized.length > 60 ? `${normalized.slice(0, 57)}...` : normalized || t('chat.newChat')
}

function renderMarkdown(content: string): string {
  return renderChatMarkdown(content)
}

function normalizeReasoningEffort(value: string): ChatReasoningEffort {
  return normalizeChatReasoningEffort(value)
}

function coerceSelectedReasoningEffort() {
  const options = availableReasoningEffortOptions.value
  if (options.some((option) => option.value === selectedReasoningEffort.value)) return
  selectedReasoningEffort.value = options[0]?.value || ''
}

function isLastAssistant(message: ChatMessage): boolean {
  const lastMessage = messages.value[messages.value.length - 1]
  return lastMessage?.role === 'assistant' && lastMessage.id === message.id
}

function isWaitingAssistant(message: ChatMessage): boolean {
  return message.id < 0 && message.role === 'assistant' && message.content === ''
}

function isStreamingAssistant(message: ChatMessage): boolean {
  return message.id < 0 && message.role === 'assistant' && message.content !== ''
}

async function copyMessage(content: string) {
  await navigator.clipboard.writeText(content)
  appStore.showSuccess(t('chat.copied'))
}

function beginRename(conversation: ChatConversation) {
  renamingId.value = conversation.id
  renameTitle.value = conversation.title
}

function cancelRename() {
  renamingId.value = null
  renameTitle.value = ''
}

async function saveRename(conversation: ChatConversation) {
  if (renamingId.value !== conversation.id) return
  const title = renameTitle.value.trim()
  cancelRename()
  if (!title || title === conversation.title) return
  try {
    const updated = await chatAPI.updateConversation(conversation.id, { title })
    upsertConversation(updated)
    if (activeConversation.value?.id === updated.id) activeConversation.value = updated
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('chat.renameFailed')))
  }
}

async function confirmDelete() {
  const target = deleteTarget.value
  if (!target) return
  deleteTarget.value = null
  try {
    await chatAPI.deleteConversation(target.id)
    try {
      await deleteChatAttachmentsForConversation(currentUserId(), target.id)
    } catch {
      appStore.showWarning(t('chat.localAttachmentDeleteFailed'))
    }
    conversations.value = conversations.value.filter((item) => item.id !== target.id)
    if (activeConversation.value?.id === target.id) await startNewChat()
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('chat.deleteFailed')))
  }
}

async function clearLocalAttachments() {
  clearLocalAttachmentsConfirmOpen.value = false
  clearingLocalAttachments.value = true
  try {
    await clearUserChatAttachments(currentUserId())
    attachmentLoadToken += 1
    releaseAllAttachmentPreviews()
    pendingAttachments.value = []
    messages.value = messages.value.map(withoutTransientAttachments)
    appStore.showSuccess(t('chat.localAttachmentsCleared'))
  } catch {
    appStore.showError(t('chat.localAttachmentDeleteFailed'))
  } finally {
    clearingLocalAttachments.value = false
  }
}

function withoutTransientAttachments(message: ChatMessage): ChatMessage {
  const metadata = { ...message.metadata }
  delete metadata.transient_attachments
  delete metadata.transient_attachment_only
  return { ...message, metadata }
}

async function scrollToBottom() {
  await nextTick()
  if (messagesEl.value) {
    messagesEl.value.scrollTop = messagesEl.value.scrollHeight
  }
}

async function initializeLocalAttachments() {
  try {
    await cleanupUserChatAttachments(currentUserId())
  } catch {
    appStore.showWarning(t('chat.localAttachmentsLoadFailed'))
  }
  if (activeConversation.value === null) {
    await restoreLocalAttachments(null, [])
  }
}

onMounted(() => {
  sidebarCollapsed.value = localStorage.getItem('chat_sidebar_collapsed') === '1'
  selectedReasoningEffort.value = normalizeReasoningEffort(localStorage.getItem('chat_last_reasoning_effort') || '')
  window.addEventListener('click', closeAttachmentMenu)
  void loadInitialData()
  void initializeLocalAttachments()
})

onUnmounted(() => {
  attachmentLoadToken += 1
  releaseAllAttachmentPreviews()
  window.removeEventListener('click', closeAttachmentMenu)
  resetAssistantDeltaQueue()
})
</script>

<style scoped>
.chat-shell {
  --chat-content-gutter: clamp(1.5rem, 5vw, 4rem);
  --chat-composer-width: 52rem;

  display: grid;
  grid-template-columns: minmax(220px, 280px) minmax(0, 1fr);
  min-height: calc(100vh - 8rem);
  overflow: hidden;
  border-radius: 0.75rem;
  border: 1px solid rgb(229 231 235);
  background: rgb(255 255 255);
}

.chat-shell.sidebar-collapsed {
  grid-template-columns: 3.75rem minmax(0, 1fr);
}

.dark .chat-shell {
  border-color: rgb(55 65 81);
  background: rgb(17 24 39);
}

.chat-history {
  display: flex;
  min-height: 0;
  flex-direction: column;
  gap: 0.25rem;
  border-right: 1px solid rgb(229 231 235);
  background: rgb(249 250 251);
}

.dark .chat-history {
  border-color: rgb(55 65 81);
  background: rgb(31 41 55);
}

.history-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
  padding: 1rem 0.875rem 0.75rem;
}

.chat-history.collapsed .history-header {
  flex-direction: column;
  justify-content: flex-start;
  padding: 0.875rem 0.5rem 0.625rem;
}

.history-brand {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: 0.65rem;
}

.history-brand-mark {
  display: inline-flex;
  height: 2.15rem;
  width: 2.15rem;
  flex: none;
  align-items: center;
  justify-content: center;
  border-radius: 0.55rem;
  background: linear-gradient(135deg, rgb(37 99 235), rgb(16 185 129));
  color: white;
  font-size: 1.1rem;
  font-weight: 800;
}

.history-brand-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 1rem;
  font-weight: 700;
  color: rgb(17 24 39);
}

.dark .history-brand-text {
  color: rgb(243 244 246);
}

.history-primary {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  padding: 0.25rem 0.75rem 0.75rem;
}

.history-command {
  display: inline-flex;
  min-width: 0;
  width: 100%;
  align-items: center;
  gap: 0.75rem;
  border-radius: 0.5rem;
  padding: 0.65rem 0.7rem;
  color: rgb(55 65 81);
  font-size: 0.925rem;
  font-weight: 600;
  text-align: left;
}

.history-command:hover,
.history-command.active {
  background: rgb(229 231 235);
  color: rgb(17 24 39);
}

.dark .history-command {
  color: rgb(209 213 219);
}

.dark .history-command:hover,
.dark .history-command.active {
  background: rgb(55 65 81);
  color: rgb(243 244 246);
}

.chat-history.collapsed .history-primary,
.chat-history.collapsed .history-footer {
  align-items: center;
  padding-left: 0.5rem;
  padding-right: 0.5rem;
}

.chat-history.collapsed .history-command,
.chat-history.collapsed .history-collapse {
  width: 2.5rem;
  height: 2.5rem;
  padding: 0;
  justify-content: center;
}

.history-list {
  min-height: 0;
  flex: 1;
  overflow-y: auto;
  padding: 0.25rem 0.5rem 0.75rem;
}

.history-section-title {
  padding: 0.75rem 0.5rem 0.45rem;
  color: rgb(107 114 128);
  font-size: 0.75rem;
  font-weight: 700;
  letter-spacing: 0;
}

.dark .history-section-title {
  color: rgb(156 163 175);
}

.history-item {
  display: grid;
  grid-template-columns: 1rem minmax(0, 1fr) auto;
  align-items: center;
  width: 100%;
  gap: 0.5rem;
  border-radius: 0.5rem;
  padding: 0.55rem 0.5rem;
  text-align: left;
  color: rgb(55 65 81);
}

.history-item:hover,
.history-item-active {
  background: rgb(229 231 235);
}

.dark .history-item {
  color: rgb(209 213 219);
}

.dark .history-item:hover,
.dark .history-item-active {
  background: rgb(55 65 81);
}

.history-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 0.875rem;
}

.history-actions {
  display: flex;
  gap: 0.25rem;
  opacity: 0;
}

.history-item:hover .history-actions,
.history-item-active .history-actions {
  opacity: 1;
}

.history-rename {
  min-width: 0;
  border-radius: 0.375rem;
  border: 1px solid rgb(209 213 219);
  background: white;
  padding: 0.25rem 0.375rem;
  font-size: 0.875rem;
}

.dark .history-rename {
  border-color: rgb(75 85 99);
  background: rgb(17 24 39);
}

.history-empty {
  padding: 1rem 0.5rem;
  text-align: center;
  font-size: 0.875rem;
  color: rgb(107 114 128);
}

.history-footer {
  margin-top: auto;
  border-top: 1px solid rgb(229 231 235);
  padding: 0.75rem;
}

.dark .history-footer {
  border-color: rgb(55 65 81);
}

.chat-main {
  display: grid;
  min-width: 0;
  grid-template-rows: auto minmax(0, 1fr) auto;
}

.chat-toolbar {
  display: grid;
  grid-template-columns: minmax(10rem, 16rem) minmax(12rem, 1fr) minmax(10rem, 12rem) auto;
  gap: 0.75rem;
  border-bottom: 1px solid rgb(229 231 235);
  padding: 0.75rem;
}

.dark .chat-toolbar {
  border-color: rgb(55 65 81);
}

.toolbar-field {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 0.25rem;
}

.toolbar-field label {
  font-size: 0.75rem;
  font-weight: 600;
  color: rgb(75 85 99);
}

.dark .toolbar-field label {
  color: rgb(156 163 175);
}

.toolbar-select,
.system-input,
.composer-input {
  width: 100%;
  border-radius: 0.5rem;
  border: 1px solid rgb(209 213 219);
  background: white;
  color: rgb(17 24 39);
}

.toolbar-select,
.toolbar-input {
  height: 2.5rem;
  padding: 0 0.75rem;
}

.system-input,
.composer-input {
  resize: none;
  padding: 0.75rem;
}

.dark .toolbar-select,
.dark .system-input,
.dark .composer-input {
  border-color: rgb(75 85 99);
  background: rgb(17 24 39);
  color: rgb(243 244 246);
}

.toolbar-refresh {
  align-self: end;
  width: 2.5rem;
  padding-left: 0;
  padding-right: 0;
}

.messages-pane {
  min-height: 0;
  overflow-y: auto;
  padding: 1.25rem var(--chat-content-gutter) 0.75rem;
}

.message-stack {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 1.4rem;
}

.message-row {
  display: flex;
  width: 100%;
}

.message-row-user {
  justify-content: flex-end;
}

.message-row-assistant {
  justify-content: stretch;
}

.message-bubble {
  max-width: min(42rem, 88%);
  border-radius: 0.85rem;
  padding: 0.8rem 1rem;
}

.chat-shell.sidebar-collapsed .message-bubble {
  max-width: min(46rem, 88%);
}

.message-user {
  background: rgb(243 244 246);
  color: rgb(17 24 39);
}

.dark .message-user {
  background: rgb(31 41 55);
  color: rgb(243 244 246);
}

.assistant-message {
  width: 100%;
  min-width: 0;
  color: rgb(17 24 39);
}

.dark .assistant-message {
  color: rgb(243 244 246);
}

.message-meta,
.message-tools {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.75rem;
  opacity: 0.78;
}

.message-content {
  overflow-wrap: anywhere;
}

.assistant-content {
  font-size: 0.975rem;
  line-height: 1.7;
}

.typing-indicator {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  min-height: 1.25rem;
  padding: 0.2rem 0;
}

.typing-indicator-inline {
  margin-top: 0.25rem;
  padding-top: 0;
}

.typing-indicator span {
  width: 0.42rem;
  height: 0.42rem;
  border-radius: 9999px;
  background: rgb(107 114 128);
  animation: typing-bounce 1.1s infinite ease-in-out;
}

.typing-indicator span:nth-child(2) {
  animation-delay: 0.14s;
}

.typing-indicator span:nth-child(3) {
  animation-delay: 0.28s;
}

.dark .typing-indicator span {
  background: rgb(209 213 219);
}

@keyframes typing-bounce {
  0%,
  80%,
  100% {
    opacity: 0.38;
    transform: translateY(0);
  }

  40% {
    opacity: 1;
    transform: translateY(-0.22rem);
  }
}

.message-content :deep(pre) {
  overflow-x: auto;
  border-radius: 0.5rem;
  background: rgb(17 24 39);
  padding: 0.75rem;
  color: rgb(243 244 246);
}

.message-content :deep(code) {
  font-size: 0.875em;
}

.message-content :deep(.katex-display) {
  overflow-x: auto;
  overflow-y: hidden;
  padding: 0.25rem 0;
}

.message-error {
  margin-top: 0.5rem;
  color: rgb(220 38 38);
  font-size: 0.8125rem;
}

.message-tools {
  margin-top: 0.5rem;
}

.assistant-tools {
  opacity: 0;
  transition: opacity 0.15s ease;
}

.message-row-assistant:hover .assistant-tools,
.assistant-tools:focus-within {
  opacity: 0.78;
}

.message-tools-user {
  justify-content: flex-end;
  color: inherit;
}

.assistant-meta {
  margin-bottom: 0.45rem;
}

.message-status.error {
  color: rgb(220 38 38);
}

.composer {
  display: flex;
  flex-direction: column;
  width: min(var(--chat-composer-width), calc(100% - (var(--chat-content-gutter) * 2)));
  margin: 0 auto 1rem;
  gap: 0.5rem;
  border: 1px solid rgb(209 213 219);
  border-radius: 1.25rem;
  background: white;
  padding: 0.55rem;
  box-shadow: 0 16px 40px rgb(15 23 42 / 0.08);
}

.dark .composer {
  border-color: rgb(55 65 81);
  background: rgb(17 24 39);
  box-shadow: 0 16px 40px rgb(0 0 0 / 0.24);
}

.composer:focus-within {
  border-color: rgb(59 130 246);
  box-shadow: 0 16px 40px rgb(37 99 235 / 0.14);
}

.dark .composer:focus-within {
  border-color: rgb(96 165 250);
  box-shadow: 0 16px 40px rgb(37 99 235 / 0.24);
}

.composer-controls {
  display: grid;
  grid-template-columns: 2.5rem minmax(0, 1fr) 2.5rem;
  align-items: end;
  gap: 0.5rem;
}

.attachment-control {
  position: relative;
}

.composer .composer-input {
  min-height: 2.5rem;
  border: 0;
  background: transparent;
  overflow-y: hidden;
  padding: 0.55rem 0.65rem;
  line-height: 1.5;
  outline: 0;
}

.dark .composer .composer-input {
  border-color: transparent;
  background: transparent;
}

.composer-button,
.composer-attach-button,
.icon-button,
.text-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
}

.composer-button,
.composer-attach-button {
  height: 2.5rem;
  width: 2.5rem;
  border-radius: 9999px;
}

.composer-button {
  background: rgb(37 99 235);
  color: white;
}

.composer-attach-button {
  background: rgb(243 244 246);
  color: rgb(55 65 81);
}

.composer-attach-button:hover {
  background: rgb(229 231 235);
}

.dark .composer-attach-button {
  background: rgb(31 41 55);
  color: rgb(209 213 219);
}

.dark .composer-attach-button:hover {
  background: rgb(55 65 81);
}

.composer-button:disabled,
.composer-attach-button:disabled {
  cursor: not-allowed;
  background: rgb(156 163 175);
  color: white;
}

.composer-button.stop {
  background: rgb(220 38 38);
}

.attachment-menu {
  position: absolute;
  bottom: calc(100% + 0.55rem);
  left: 0;
  z-index: 20;
  display: flex;
  min-width: 12rem;
  flex-direction: column;
  overflow: hidden;
  border-radius: 0.75rem;
  border: 1px solid rgb(229 231 235);
  background: white;
  box-shadow: 0 18px 45px rgb(15 23 42 / 0.16);
}

.dark .attachment-menu {
  border-color: rgb(55 65 81);
  background: rgb(17 24 39);
  box-shadow: 0 18px 45px rgb(0 0 0 / 0.32);
}

.attachment-menu-item {
  display: flex;
  align-items: center;
  gap: 0.65rem;
  padding: 0.75rem 0.9rem;
  color: rgb(55 65 81);
  font-size: 0.875rem;
  font-weight: 600;
  text-align: left;
}

.attachment-menu-item:hover {
  background: rgb(243 244 246);
}

.dark .attachment-menu-item {
  color: rgb(229 231 235);
}

.dark .attachment-menu-item:hover {
  background: rgb(31 41 55);
}

.attachment-strip,
.message-attachments {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}

.attachment-strip {
  padding: 0.1rem 0.15rem 0;
}

.attachment-chip,
.message-attachment {
  display: inline-flex;
  max-width: 14rem;
  align-items: center;
  gap: 0.5rem;
  overflow: hidden;
  border-radius: 0.65rem;
  border: 1px solid rgb(229 231 235);
  background: rgb(249 250 251);
  color: rgb(55 65 81);
  font-size: 0.8125rem;
}

.attachment-chip {
  padding: 0.25rem 0.35rem 0.25rem 0.45rem;
}

.message-attachment {
  margin-top: 0.65rem;
  padding: 0.35rem 0.55rem;
}

.dark .attachment-chip,
.dark .message-attachment {
  border-color: rgb(75 85 99);
  background: rgb(17 24 39);
  color: rgb(209 213 219);
}

.attachment-name,
.message-attachment span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.attachment-thumb,
.message-attachment-image {
  flex: none;
  border-radius: 0.45rem;
  object-fit: cover;
}

.attachment-thumb {
  height: 2rem;
  width: 2rem;
}

.message-attachment-image {
  height: 3rem;
  width: 3rem;
}

.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  clip-path: inset(50%);
}

.icon-button {
  height: 1.5rem;
  width: 1.5rem;
  border-radius: 0.375rem;
}

.icon-button:hover {
  background: rgb(209 213 219);
}

.icon-button.danger:hover {
  color: rgb(220 38 38);
}

.text-button {
  gap: 0.25rem;
  font-size: 0.75rem;
}

.text-button:hover {
  text-decoration: underline;
}

@media (hover: none) {
  .assistant-tools {
    opacity: 0.78;
  }
}

.empty-state {
  display: flex;
  min-height: 20rem;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 1rem;
  color: rgb(107 114 128);
  text-align: center;
}

.empty-state h2 {
  font-size: 1.125rem;
  font-weight: 600;
  color: rgb(55 65 81);
}

.dark .empty-state h2 {
  color: rgb(209 213 219);
}

.settings-backdrop {
  position: fixed;
  inset: 0;
  z-index: 50;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 1rem;
  background: rgb(17 24 39 / 0.42);
}

.settings-dialog {
  width: min(36rem, 100%);
  overflow: hidden;
  border-radius: 0.75rem;
  border: 1px solid rgb(229 231 235);
  background: white;
  box-shadow:
    0 20px 25px -5px rgb(0 0 0 / 0.1),
    0 8px 10px -6px rgb(0 0 0 / 0.1);
}

.dark .settings-dialog {
  border-color: rgb(55 65 81);
  background: rgb(17 24 39);
  color: rgb(243 244 246);
}

.settings-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  border-bottom: 1px solid rgb(229 231 235);
  padding: 0.875rem 1rem;
}

.dark .settings-header {
  border-color: rgb(55 65 81);
}

.settings-header h2 {
  font-size: 1rem;
  font-weight: 600;
}

.settings-field {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  padding: 1rem;
}

.settings-field > span {
  font-size: 0.875rem;
  font-weight: 600;
  color: rgb(55 65 81);
}

.dark .settings-field > span {
  color: rgb(209 213 219);
}

.settings-export {
  align-self: flex-start;
}

.settings-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
  border-top: 1px solid rgb(229 231 235);
  padding: 0.75rem 1rem;
}

.dark .settings-actions {
  border-color: rgb(55 65 81);
}

@media (max-width: 900px) {
  .chat-shell {
    --chat-content-gutter: clamp(1.25rem, 4vw, 2rem);
  }

  .chat-shell,
  .chat-shell.sidebar-collapsed {
    grid-template-columns: 1fr;
  }

  .chat-history {
    max-height: 14rem;
    border-right: 0;
    border-bottom: 1px solid rgb(229 231 235);
  }

  .chat-history.collapsed {
    max-height: 3.75rem;
  }

  .chat-history.collapsed .history-header {
    flex-direction: row;
    justify-content: space-between;
  }

  .chat-toolbar {
    grid-template-columns: 1fr;
  }

  .toolbar-refresh {
    width: 100%;
  }
}
</style>
