import type { ChatReasoningEffort } from '@/api/chat'

export type ReasoningEffortOption = { value: ChatReasoningEffort; label: string }

const defaultReasoningEffortOptions: ReasoningEffortOption[] = [
  { value: '', label: 'chat.reasoningDefault' },
  { value: 'none', label: 'chat.reasoningNone' },
  { value: 'minimal', label: 'chat.reasoningMinimal' },
  { value: 'low', label: 'chat.reasoningLow' },
  { value: 'medium', label: 'chat.reasoningMedium' },
  { value: 'high', label: 'chat.reasoningHigh' },
  { value: 'max', label: 'chat.reasoningMax' },
  { value: 'xhigh', label: 'chat.reasoningXHigh' }
]

const gpt54PlusReasoningEffortOptions: ReasoningEffortOption[] = [
  { value: 'none', label: 'chat.reasoningNone' },
  { value: 'low', label: 'chat.reasoningLow' },
  { value: 'medium', label: 'chat.reasoningMedium' },
  { value: 'high', label: 'chat.reasoningHigh' },
  { value: 'xhigh', label: 'chat.reasoningXHigh' }
]

const gpt56ReasoningEffortOptions: ReasoningEffortOption[] = [
  { value: 'none', label: 'chat.reasoningNone' },
  { value: 'low', label: 'chat.reasoningLow' },
  { value: 'medium', label: 'chat.reasoningMedium' },
  { value: 'high', label: 'chat.reasoningHigh' },
  { value: 'max', label: 'chat.reasoningMax' },
  { value: 'xhigh', label: 'chat.reasoningXHigh' }
]

export function normalizeChatReasoningEffort(value: string): ChatReasoningEffort {
  const normalized = value.trim().toLowerCase().replace(/_/g, '-')
  if (normalized === 'x-high') return 'xhigh'
  return defaultReasoningEffortOptions.some((option) => option.value === normalized)
    ? normalized as ChatReasoningEffort
    : ''
}

export function chatReasoningEffortOptionsForModel(model: string): ReasoningEffortOption[] {
  const normalized = model.trim().toLowerCase()
  const unqualified = normalized.split('/').pop() || normalized
  if (unqualified.startsWith('gpt-5.6')) {
    return gpt56ReasoningEffortOptions
  }
  if (
    unqualified.startsWith('gpt-5.4') ||
    unqualified.startsWith('gpt-5.5')
  ) {
    return gpt54PlusReasoningEffortOptions
  }
  return defaultReasoningEffortOptions
}
