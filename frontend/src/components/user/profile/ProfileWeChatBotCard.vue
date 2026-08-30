<template>
  <div class="card">
    <div class="flex flex-col gap-3 border-b border-gray-100 px-6 py-4 sm:flex-row sm:items-start sm:justify-between dark:border-dark-700">
      <div>
        <div class="flex items-center gap-2">
          <Icon name="chat" size="md" class="text-green-600 dark:text-green-400" />
          <h2 class="text-lg font-medium text-gray-900 dark:text-white">
            {{ t('profile.wechatBot.title') }}
          </h2>
        </div>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('profile.wechatBot.description') }}
        </p>
      </div>
      <button
        type="button"
        class="btn btn-secondary btn-sm self-start"
        :disabled="loading"
        :title="t('common.refresh')"
        @click="load"
      >
        <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
        <span>{{ t('common.refresh') }}</span>
      </button>
    </div>

    <div v-if="loading && !status" class="flex items-center justify-center px-6 py-10 text-gray-500">
      <Icon name="refresh" size="md" class="mr-2 animate-spin" />
      {{ t('common.loading') }}
    </div>

    <div v-else-if="!status?.available" class="px-6 py-6">
      <div class="flex items-start gap-3 rounded-md border border-amber-200 bg-amber-50 p-4 dark:border-amber-800 dark:bg-amber-900/20">
        <Icon name="exclamationTriangle" size="md" class="mt-0.5 shrink-0 text-amber-500" />
        <p class="text-sm text-amber-800 dark:text-amber-200">
          {{ t('profile.wechatBot.unavailable') }}
        </p>
      </div>
    </div>

    <div v-else-if="!status.logged_in" class="space-y-5 px-6 py-6">
      <div>
        <h3 class="text-sm font-medium text-gray-900 dark:text-white">
          {{ t('profile.wechatBot.connectTitle') }}
        </h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('profile.wechatBot.connectHint') }}
        </p>
      </div>

      <div v-if="status.last_error" class="flex items-start gap-3 rounded-md border border-red-200 bg-red-50 p-4 dark:border-red-800 dark:bg-red-900/20">
        <Icon name="exclamationCircle" size="md" class="mt-0.5 shrink-0 text-red-500" />
        <p class="min-w-0 break-words text-sm text-red-700 dark:text-red-300">
          {{ status.last_error }}
        </p>
      </div>

      <div v-if="loginQR" class="flex flex-col gap-5 rounded-md border border-gray-200 p-4 sm:flex-row sm:items-center dark:border-dark-600">
        <div class="flex h-[224px] w-[224px] shrink-0 items-center justify-center overflow-hidden rounded-md bg-white p-2">
          <img
            v-if="qrImage"
            :src="qrImage"
            :alt="t('profile.wechatBot.qrAlt')"
            class="h-full w-full object-contain"
          />
          <Icon v-else name="refresh" size="lg" class="animate-spin text-gray-400" />
        </div>
        <div class="min-w-0">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ loginStatusLabel }}</p>
          <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">
            {{ t('profile.wechatBot.qrHint') }}
          </p>
          <p class="mt-3 text-xs text-gray-500 dark:text-gray-400">
            {{ t('profile.wechatBot.qrExpires', { time: formatTime(loginQR.expires_at) }) }}
          </p>
          <button type="button" class="btn btn-secondary btn-sm mt-4" :disabled="startingLogin" @click="startLogin">
            <Icon name="refresh" size="sm" />
            {{ t('profile.wechatBot.newQR') }}
          </button>
        </div>
      </div>

      <button
        v-else
        data-testid="wechat-bot-connect"
        type="button"
        class="btn btn-primary"
        :disabled="startingLogin"
        @click="startLogin"
      >
        <Icon name="login" size="sm" />
        {{ startingLogin ? t('common.loading') : t('profile.wechatBot.connect') }}
      </button>
    </div>

    <div v-else class="space-y-6 px-6 py-6">
      <div class="flex flex-col gap-3 border-b border-gray-100 pb-5 sm:flex-row sm:items-start sm:justify-between dark:border-dark-700">
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2">
            <span class="inline-flex items-center gap-1.5 rounded-full bg-green-50 px-2.5 py-1 text-xs font-medium text-green-700 dark:bg-green-900/30 dark:text-green-300">
              <span class="h-1.5 w-1.5 rounded-full bg-green-500"></span>
              {{ t('profile.wechatBot.connected') }}
            </span>
            <span
              class="inline-flex items-center rounded-full px-2.5 py-1 text-xs font-medium"
              :class="status.delivery_open
                ? 'bg-blue-50 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
                : 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'"
            >
              {{ status.delivery_open ? t('profile.wechatBot.windowOpen') : t('profile.wechatBot.windowClosed') }}
            </span>
          </div>
          <p v-if="status.last_connected_at" class="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {{ t('profile.wechatBot.lastConnected', { time: formatTime(status.last_connected_at) }) }}
          </p>
          <p v-if="status.bot_id" class="mt-1 break-all font-mono text-xs text-gray-500 dark:text-gray-400">
            Bot ID: {{ status.bot_id }}
          </p>
        </div>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn btn-secondary btn-sm" :disabled="testing" @click="sendTest">
            <Icon name="bell" size="sm" />
            {{ t('profile.wechatBot.test') }}
          </button>
          <button type="button" class="btn btn-secondary btn-sm text-red-600 dark:text-red-400" @click="showDisconnect = true">
            <Icon name="trash" size="sm" />
            {{ t('profile.wechatBot.disconnect') }}
          </button>
        </div>
      </div>

      <div
        v-if="!status.delivery_open"
        class="flex items-start gap-3 rounded-md border border-amber-200 bg-amber-50 p-4 dark:border-amber-800 dark:bg-amber-900/20"
      >
        <Icon name="exclamationTriangle" size="md" class="mt-0.5 shrink-0 text-amber-500" />
        <p class="text-sm text-amber-800 dark:text-amber-200">
          {{ t('profile.wechatBot.activateHint') }}
        </p>
      </div>

      <div class="space-y-4">
        <SettingToggle
          v-model="form.enabled"
          :label="t('profile.wechatBot.enabled')"
          :hint="t('profile.wechatBot.enabledHint')"
        />
        <div class="grid grid-cols-1 gap-4 border-t border-gray-100 pt-4 md:grid-cols-3 dark:border-dark-700">
          <SettingToggle v-model="form.notify_admin" :label="t('profile.wechatBot.adminNotify')" />
          <SettingToggle v-model="form.notify_balance" :label="t('profile.wechatBot.balanceNotify')" />
          <SettingToggle v-model="form.notify_login" :label="t('profile.wechatBot.loginNotify')" />
        </div>
      </div>

      <div class="space-y-4 border-t border-gray-100 pt-5 dark:border-dark-700">
        <SettingToggle
          v-model="form.chat_enabled"
          :label="t('profile.wechatBot.chatEnabled')"
          :hint="t('profile.wechatBot.chatHint')"
        />
        <div v-if="form.chat_enabled" class="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div>
            <label class="input-label">{{ t('profile.wechatBot.apiKey') }}</label>
            <select v-model="form.chat_api_key_id" class="input" :disabled="keysLoading">
              <option :value="null">{{ t('profile.wechatBot.selectKey') }}</option>
              <option v-for="key in usableKeys" :key="key.id" :value="key.id">
                {{ key.name }} (#{{ key.id }})
              </option>
            </select>
            <p v-if="!keysLoading && usableKeys.length === 0" class="mt-1 text-xs text-amber-600 dark:text-amber-400">
              {{ t('profile.wechatBot.noKeys') }}
            </p>
          </div>
          <div>
            <label class="input-label">{{ t('profile.wechatBot.model') }}</label>
            <input v-model.trim="form.chat_model" class="input font-mono text-sm" maxlength="160" placeholder="gpt-4o-mini" />
          </div>
        </div>
      </div>

      <div class="flex justify-end">
        <button type="button" class="btn btn-primary" :disabled="saving" @click="save">
          <Icon v-if="saving" name="refresh" size="sm" class="animate-spin" />
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </div>

    <ConfirmDialog
      :show="showDisconnect"
      :title="t('profile.wechatBot.disconnectTitle')"
      :message="t('profile.wechatBot.disconnectConfirm')"
      :confirm-text="t('profile.wechatBot.disconnect')"
      danger
      @confirm="disconnect"
      @cancel="showDisconnect = false"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import { keysAPI, weChatBotAPI } from '@/api'
import type {
  WeChatBotLoginQR,
  WeChatBotSettingsUpdate,
  WeChatBotUserStatus,
} from '@/api/wechatBot'
import type { ApiKey } from '@/types'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import { Icon } from '@/components/icons'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const SettingToggle = Toggle
const { t, locale } = useI18n()
const appStore = useAppStore()

const status = ref<WeChatBotUserStatus | null>(null)
const loginQR = ref<WeChatBotLoginQR | null>(null)
const qrImage = ref('')
const loginState = ref('wait')
const apiKeys = ref<ApiKey[]>([])
const loading = ref(false)
const keysLoading = ref(false)
const startingLogin = ref(false)
const saving = ref(false)
const testing = ref(false)
const showDisconnect = ref(false)
let pollTimer: ReturnType<typeof setTimeout> | null = null

const form = reactive<WeChatBotSettingsUpdate>({
  enabled: true,
  notify_admin: true,
  notify_balance: true,
  notify_login: true,
  chat_enabled: false,
  chat_api_key_id: null,
  chat_model: 'gpt-4o-mini',
})

const usableKeys = computed(() => apiKeys.value.filter((key) => {
  if (key.status !== 'active') return false
  if (key.expires_at && new Date(key.expires_at).getTime() <= Date.now()) return false
  return key.quota <= 0 || key.quota_used < key.quota
}))

const loginStatusLabel = computed(() => {
  const key = ['scaned', 'scaned_but_redirect'].includes(loginState.value)
    ? 'scanned'
    : loginState.value === 'expired'
      ? 'expired'
      : 'waiting'
  return t(`profile.wechatBot.qrStatus.${key}`)
})

function clearPolling(): void {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = null
}

function schedulePoll(): void {
  clearPolling()
  pollTimer = setTimeout(() => void pollLogin(), 2000)
}

function applyStatus(value: WeChatBotUserStatus): void {
  status.value = value
  form.enabled = value.enabled
  form.notify_admin = value.notify_admin
  form.notify_balance = value.notify_balance
  form.notify_login = value.notify_login
  form.chat_enabled = value.chat_enabled
  form.chat_api_key_id = value.chat_api_key_id ?? null
  form.chat_model = value.chat_model || 'gpt-4o-mini'
  if (value.logged_in) {
    clearPolling()
    loginQR.value = null
    qrImage.value = ''
  }
}

async function load(): Promise<void> {
  loading.value = true
  try {
    applyStatus(await weChatBotAPI.getUserStatus())
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('profile.wechatBot.loadFailed')))
  } finally {
    loading.value = false
  }
}

async function loadKeys(): Promise<void> {
  keysLoading.value = true
  try {
    const result = await keysAPI.list(1, 100)
    apiKeys.value = result.items
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('profile.wechatBot.keysFailed')))
  } finally {
    keysLoading.value = false
  }
}

async function startLogin(): Promise<void> {
  startingLogin.value = true
  clearPolling()
  try {
    const result = await weChatBotAPI.createLoginQR()
    loginQR.value = result
    loginState.value = 'wait'
    const qrContent = result.qrcode_img_content || result.url || result.qrcode
    qrImage.value = await QRCode.toDataURL(qrContent, { width: 224, margin: 1 })
    schedulePoll()
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('profile.wechatBot.qrFailed')))
  } finally {
    startingLogin.value = false
  }
}

async function pollLogin(): Promise<void> {
  if (!loginQR.value) return
  try {
    const result = await weChatBotAPI.pollLoginQR(loginQR.value.login_id)
    loginState.value = result.status
    if (result.logged_in || result.status === 'confirmed') {
      await load()
      appStore.showSuccess(t('profile.wechatBot.loginSuccess'))
      return
    }
    if (result.status !== 'expired') schedulePoll()
  } catch (error: unknown) {
    clearPolling()
    appStore.showError(extractApiErrorMessage(error, t('profile.wechatBot.pollFailed')))
  }
}

async function save(): Promise<void> {
  if (form.chat_enabled && !form.chat_api_key_id) {
    appStore.showError(t('profile.wechatBot.selectKeyRequired'))
    return
  }
  saving.value = true
  try {
    applyStatus(await weChatBotAPI.updateUserSettings({ ...form }))
    appStore.showSuccess(t('common.saved'))
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('profile.wechatBot.saveFailed')))
  } finally {
    saving.value = false
  }
}

async function sendTest(): Promise<void> {
  testing.value = true
  try {
    await weChatBotAPI.sendTestNotification()
    appStore.showSuccess(t('profile.wechatBot.testQueued'))
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('profile.wechatBot.testFailed')))
  } finally {
    testing.value = false
  }
}

async function disconnect(): Promise<void> {
  showDisconnect.value = false
  try {
    await weChatBotAPI.disconnect()
    clearPolling()
    await load()
    appStore.showSuccess(t('profile.wechatBot.disconnected'))
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('profile.wechatBot.disconnectFailed')))
  }
}

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale.value)
}

onMounted(() => {
  void Promise.all([load(), loadKeys()])
})
onBeforeUnmount(clearPolling)
</script>
