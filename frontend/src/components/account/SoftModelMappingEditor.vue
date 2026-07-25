<template>
  <div class="space-y-3">
    <div>
      <label class="input-label">{{ t('admin.accounts.softModelMapping') }}</label>
      <p class="input-hint">{{ t('admin.accounts.softModelMappingDesc') }}</p>
    </div>

    <div v-if="modelValue.length > 0" class="space-y-2">
      <div
        v-for="(mapping, index) in modelValue"
        :key="rowKey(mapping, index)"
        class="flex items-center gap-2"
      >
        <input
          :value="mapping.from"
          type="text"
          class="input flex-1"
          :placeholder="t('admin.accounts.requestModel')"
          @input="updateRow(index, 'from', ($event.target as HTMLInputElement).value)"
        />
        <svg
          class="h-4 w-4 flex-shrink-0 text-gray-400"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M14 5l7 7m0 0l-7 7m7-7H3"
          />
        </svg>
        <input
          :value="mapping.to"
          type="text"
          class="input flex-1"
          :placeholder="t('admin.accounts.softFallbackModel')"
          @input="updateRow(index, 'to', ($event.target as HTMLInputElement).value)"
        />
        <button
          type="button"
          class="rounded-lg p-2 text-red-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20"
          @click="removeRow(index)"
        >
          <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
            />
          </svg>
        </button>
      </div>
    </div>

    <button
      type="button"
      class="w-full rounded-lg border-2 border-dashed border-gray-300 px-4 py-2 text-gray-600 transition-colors hover:border-gray-400 hover:text-gray-700 dark:border-dark-500 dark:text-gray-400 dark:hover:border-dark-400 dark:hover:text-gray-300"
      @click="addRow"
    >
      {{ t('admin.accounts.addSoftMapping') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

export interface SoftModelMappingEntry {
  from: string
  to: string
}

const props = defineProps<{
  modelValue: SoftModelMappingEntry[]
}>()

const emit = defineEmits<{
  'update:modelValue': [value: SoftModelMappingEntry[]]
}>()

const { t } = useI18n()

const rowKey = (mapping: SoftModelMappingEntry, index: number) =>
  `${index}:${mapping.from}:${mapping.to}`

const updateRow = (index: number, field: 'from' | 'to', value: string) => {
  const next = props.modelValue.map((row, i) =>
    i === index ? { ...row, [field]: value } : row
  )
  emit('update:modelValue', next)
}

const addRow = () => {
  emit('update:modelValue', [...props.modelValue, { from: '', to: '' }])
}

const removeRow = (index: number) => {
  emit(
    'update:modelValue',
    props.modelValue.filter((_, i) => i !== index)
  )
}
</script>
