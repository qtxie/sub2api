<template>
  <div class="card">
    <div class="flex flex-col gap-3 border-b border-gray-100 px-6 py-4 sm:flex-row sm:items-start sm:justify-between dark:border-dark-700">
      <div>
        <div class="flex items-center gap-2">
          <Icon name="chat" size="md" class="text-green-600 dark:text-green-400" />
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
            {{ t('admin.settings.wechatBot.title') }}
          </h2>
        </div>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.settings.wechatBot.description') }}
        </p>
      </div>
      <button
        type="button"
        class="btn btn-secondary btn-sm self-start"
        :disabled="loading"
        :title="t('common.refresh')"
        @click="loadStatus"
      >
        <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
        <span>{{ t('common.refresh') }}</span>
      </button>
    </div>

    <div class="space-y-6 px-6 py-6">
      <div v-if="loading && !status" class="flex items-center justify-center py-8 text-gray-500">
        <Icon name="refresh" size="md" class="mr-2 animate-spin" />
        {{ t('common.loading') }}
      </div>

      <template v-else>
        <div class="grid grid-cols-2 gap-px overflow-hidden rounded-md border border-gray-200 bg-gray-200 sm:grid-cols-5 dark:border-dark-600 dark:bg-dark-600">
          <div class="bg-white px-4 py-3 dark:bg-dark-800">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.wechatBot.worker') }}</p>
            <p class="mt-1 text-sm font-medium" :class="status?.running ? 'text-green-600 dark:text-green-400' : 'text-red-600 dark:text-red-400'">
              {{ status?.running ? t('admin.settings.wechatBot.running') : t('admin.settings.wechatBot.stopped') }}
            </p>
          </div>
          <div class="bg-white px-4 py-3 dark:bg-dark-800">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.wechatBot.connectedUsers') }}</p>
            <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ status?.connected_users ?? 0 }}</p>
          </div>
          <div class="bg-white px-4 py-3 dark:bg-dark-800">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.wechatBot.deliveryOpenUsers') }}</p>
            <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ status?.delivery_open_users ?? 0 }}</p>
          </div>
          <div class="bg-white px-4 py-3 dark:bg-dark-800">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.wechatBot.pending') }}</p>
            <p class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ status?.pending_messages ?? 0 }}</p>
          </div>
          <div class="col-span-2 bg-white px-4 py-3 sm:col-span-1 dark:bg-dark-800">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.wechatBot.usersWithErrors') }}</p>
            <p
              class="mt-1 text-sm font-medium"
              :class="status?.users_with_errors ? 'text-red-600 dark:text-red-400' : 'text-gray-900 dark:text-white'"
            >
              {{ status?.users_with_errors ?? 0 }}
            </p>
          </div>
        </div>

        <div class="space-y-4 border-t border-gray-100 pt-5 dark:border-dark-700">
          <div>
            <h3 class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.settings.wechatBot.broadcastTitle') }}</h3>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.wechatBot.broadcastHint') }}</p>
          </div>
          <div class="grid grid-cols-1 gap-4">
            <div>
              <label class="input-label">{{ t('admin.settings.wechatBot.messageTitle') }}</label>
              <input
                v-model.trim="broadcastTitle"
                class="input"
                maxlength="200"
                :placeholder="t('admin.settings.wechatBot.titlePlaceholder')"
                @keydown.enter.prevent="sendBroadcast"
              />
            </div>
            <div>
              <label class="input-label">{{ t('admin.settings.wechatBot.message') }}</label>
              <textarea
                v-model.trim="broadcastMessage"
                data-testid="wechat-bot-broadcast-message"
                class="input min-h-28 resize-y"
                maxlength="4500"
                :placeholder="t('admin.settings.wechatBot.messagePlaceholder')"
              ></textarea>
              <p class="mt-1 text-right text-xs text-gray-400">{{ broadcastMessage.length }} / 4500</p>
            </div>
          </div>
          <div class="flex justify-end">
            <button
              data-testid="wechat-bot-broadcast"
              type="button"
              class="btn btn-primary"
              :disabled="broadcasting || !broadcastMessage"
              @click="sendBroadcast"
            >
              <Icon v-if="broadcasting" name="refresh" size="sm" class="animate-spin" />
              <Icon v-else name="bell" size="sm" />
              {{ t('admin.settings.wechatBot.sendBroadcast') }}
            </button>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { weChatBotAPI } from '@/api'
import type { WeChatBotAdminStatus } from '@/api/wechatBot'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()
const status = ref<WeChatBotAdminStatus | null>(null)
const loading = ref(false)
const broadcasting = ref(false)
const broadcastTitle = ref('')
const broadcastMessage = ref('')

async function loadStatus(): Promise<void> {
  loading.value = true
  try {
    status.value = await weChatBotAPI.getAdminStatus()
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('admin.settings.wechatBot.statusFailed')))
  } finally {
    loading.value = false
  }
}

async function sendBroadcast(): Promise<void> {
  if (!broadcastMessage.value) return
  broadcasting.value = true
  try {
    const result = await weChatBotAPI.broadcast({
      title: broadcastTitle.value,
      message: broadcastMessage.value,
    })
    broadcastTitle.value = ''
    broadcastMessage.value = ''
    appStore.showSuccess(t('admin.settings.wechatBot.broadcastQueued', { count: result.queued }))
    await loadStatus()
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('admin.settings.wechatBot.broadcastFailed')))
  } finally {
    broadcasting.value = false
  }
}

onMounted(() => void loadStatus())
</script>
