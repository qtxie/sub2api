<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="py-2">
          <div class="flex flex-wrap items-end gap-3">
            <div class="w-full sm:w-64">
              <label class="input-label">{{ t('admin.userSessions.filterUser') }}</label>
              <input
                v-model="userFilter"
                type="number"
                min="1"
                class="input"
                :placeholder="t('admin.userSessions.filterUserPlaceholder')"
                @keyup.enter="search"
              />
            </div>
            <button class="btn btn-primary" :disabled="loading" @click="search">{{ t('common.search') }}</button>
            <button class="btn btn-secondary" :disabled="loading" @click="reset">{{ t('common.reset') }}</button>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="sessions" :loading="loading" row-key="id">
          <template #cell-id="{ row }">
            <span class="font-mono text-sm">#{{ row.id }}</span>
          </template>
          <template #cell-user_id="{ row }">
            <div class="font-medium text-gray-900 dark:text-white">#{{ row.user_id }}</div>
            <div class="text-xs text-gray-400">API Key #{{ row.api_key_id || '—' }}</div>
          </template>
          <template #cell-protocol="{ row }">
            <div>{{ row.protocol || '—' }}</div>
            <div class="max-w-56 truncate text-xs text-gray-400" :title="row.model">{{ row.model || '—' }}</div>
          </template>
          <template #cell-counts="{ row }">
            {{ t('admin.userSessions.turns', { turns: row.turn_count, requests: row.request_count }) }}
          </template>
          <template #cell-updated_at="{ value }">
            <span class="whitespace-nowrap text-sm text-gray-500">{{ formatTime(value) }}</span>
          </template>
          <template #cell-actions="{ row }">
            <div class="flex flex-wrap gap-2">
              <button class="text-sm font-medium text-primary-600" @click="openDetail(row.id)">{{ t('admin.userSessions.view') }}</button>
              <button class="text-sm font-medium text-gray-600 dark:text-gray-300" @click="exportArchive(row.id)">{{ t('admin.userSessions.export') }}</button>
              <button class="text-sm font-medium text-red-600" @click="askDelete(row.id)">{{ t('admin.userSessions.delete') }}</button>
            </div>
          </template>
          <template #empty>
            <div class="py-10 text-center text-sm text-gray-500">{{ t('admin.userSessions.empty') }}</div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="total > 0"
          :total="total"
          :page="page"
          :page-size="pageSize"
          @update:page="changePage"
          @update:pageSize="changePageSize"
        />
      </template>
    </TablePageLayout>

    <BaseDialog
      :show="detailVisible"
      :title="t('admin.userSessions.detailTitle', { id: detail?.id || '' })"
      width="wide"
      @close="detailVisible = false"
    >
      <div v-if="detailLoading" class="py-16 text-center text-gray-500">{{ t('common.loading') }}</div>
      <div v-else-if="detail" class="max-h-[70vh] divide-y divide-gray-200 overflow-y-auto pr-1 dark:divide-dark-700">
        <div
          v-for="turn in detail.turns"
          :key="turn.id"
          class="py-4 first:pt-0 last:pb-0"
        >
          <div class="mb-3 flex items-center justify-between gap-3">
            <span class="rounded-full bg-primary-50 px-2.5 py-1 text-xs font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
              {{ turn.ordinal }} · {{ turn.role }}
            </span>
            <span class="max-w-64 truncate font-mono text-[10px] text-gray-400" :title="turn.branch_key">{{ turn.branch_key }}</span>
          </div>
          <div class="space-y-3">
            <div v-for="part in turn.parts" :key="part.id" class="border-l-2 border-gray-200 py-1 pl-3 dark:border-dark-700">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <span class="text-xs text-gray-500">
                  {{ t('admin.userSessions.partMeta', { kind: part.kind, size: part.byte_length, mime: part.detected_mime }) }}
                </span>
                <button
                  v-if="part.kind !== 'text'"
                  class="text-xs font-medium text-primary-600"
                  @click="downloadPart(detail!.id, part)"
                >
                  {{ t('admin.userSessions.download') }}
                </button>
              </div>
              <pre v-if="part.kind === 'text'" class="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-words text-sm text-gray-800 dark:text-gray-200">{{ part.text || '' }}</pre>
              <div v-else class="mt-1 break-all text-xs text-gray-400">{{ part.original_filename || part.source_path }}</div>
            </div>
          </div>
        </div>
      </div>
    </BaseDialog>

    <ConfirmDialog
      :show="deleteID !== null"
      :title="t('admin.userSessions.deleteTitle')"
      :message="t('admin.userSessions.deleteMessage', { id: deleteID || '' })"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="confirmDelete"
      @cancel="deleteID = null"
    />
    <TotpStepUpDialog :controller="stepUp" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { SessionArchiveDetail, SessionArchivePart, SessionArchiveSummary } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { useStepUp, isStepUpBlocked, isStepUpCancelled } from '@/composables/useStepUp'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'

const { t } = useI18n()
const appStore = useAppStore()
const sessions = ref<SessionArchiveSummary[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const userFilter = ref('')
const loading = ref(false)
const detail = ref<SessionArchiveDetail | null>(null)
const detailVisible = ref(false)
const detailLoading = ref(false)
const deleteID = ref<number | null>(null)
const stepUp = useStepUp()

const columns = computed(() => [
  { key: 'id', label: t('admin.userSessions.columns.id') },
  { key: 'user_id', label: t('admin.userSessions.columns.user') },
  { key: 'protocol', label: t('admin.userSessions.columns.protocol') },
  { key: 'counts', label: t('admin.userSessions.columns.counts') },
  { key: 'updated_at', label: t('admin.userSessions.columns.updated') },
  { key: 'actions', label: t('admin.userSessions.columns.actions'), sortable: false }
])

async function load() {
  loading.value = true
  try {
    const parsedUserID = Number(userFilter.value)
    const result = await adminAPI.sessionArchive.list({
      page: page.value,
      page_size: pageSize.value,
      user_id: Number.isInteger(parsedUserID) && parsedUserID > 0 ? parsedUserID : undefined
    })
    sessions.value = result.items || []
    total.value = result.total || 0
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.userSessions.errors.list'))
  } finally {
    loading.value = false
  }
}

function search() { page.value = 1; void load() }
function reset() { userFilter.value = ''; search() }
function changePage(value: number) { page.value = value; void load() }
function changePageSize(value: number) { pageSize.value = value; page.value = 1; void load() }

async function openDetail(id: number) {
  detailVisible.value = true
  detailLoading.value = true
  try { detail.value = await adminAPI.sessionArchive.get(id) }
  catch (error: any) { appStore.showError(error?.message || t('admin.userSessions.errors.detail')); detailVisible.value = false }
  finally { detailLoading.value = false }
}

function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.click()
  URL.revokeObjectURL(url)
}

async function downloadPart(sessionID: number, part: SessionArchivePart) {
  try { saveBlob(await adminAPI.sessionArchive.downloadBlob(sessionID, part.blob_id), part.original_filename || `content-${part.blob_id}`) }
  catch (error: any) { appStore.showError(error?.message || t('admin.userSessions.errors.download')) }
}

async function exportArchive(id: number) {
  try { saveBlob(await adminAPI.sessionArchive.exportSession(id), `session-${id}.zip`) }
  catch (error: any) { appStore.showError(error?.message || t('admin.userSessions.errors.export')) }
}

function askDelete(id: number) { deleteID.value = id }
async function confirmDelete() {
  if (deleteID.value === null) return
  const id = deleteID.value
  try {
    await stepUp.run(() => adminAPI.sessionArchive.deleteSession(id))
    deleteID.value = null
    if (detail.value?.id === id) detailVisible.value = false
    appStore.showSuccess(t('admin.userSessions.deleted'))
    await load()
  } catch (error: any) {
    if (!isStepUpCancelled(error) && !isStepUpBlocked(error)) appStore.showError(error?.message || t('admin.userSessions.errors.delete'))
  }
}

function formatTime(value: string) { return value ? new Date(value).toLocaleString() : '—' }
onMounted(load)
</script>
