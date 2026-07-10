import { describe, expect, it } from 'vitest'

import {
  chatReasoningEffortOptionsForModel,
  normalizeChatReasoningEffort
} from '@/utils/chatReasoning'

describe('chat reasoning effort options', () => {
  it('uses the GPT-5.5 options for GPT-5.6 models', () => {
    const gpt55Values = chatReasoningEffortOptionsForModel('gpt-5.5').map((option) => option.value)
    const gpt56Values = chatReasoningEffortOptionsForModel('GPT-5.6-pro').map((option) => option.value)

    expect(gpt56Values).toEqual(gpt55Values)
    expect(gpt56Values).toEqual(['none', 'low', 'medium', 'high', 'xhigh'])
  })

  it('drops obsolete GPT-5.6-only saved values', () => {
    expect(normalizeChatReasoningEffort('auto')).toBe('')
    expect(normalizeChatReasoningEffort('ultra')).toBe('')
    expect(normalizeChatReasoningEffort('x-high')).toBe('xhigh')
  })
})
