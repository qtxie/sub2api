import { describe, expect, it } from 'vitest'

import {
  chatReasoningEffortOptionsForModel,
  normalizeChatReasoningEffort
} from '@/utils/chatReasoning'

describe('chat reasoning effort options', () => {
  it('adds max for GPT-5.6 models', () => {
    const gpt55Values = chatReasoningEffortOptionsForModel('gpt-5.5').map((option) => option.value)
    const expected = ['none', 'low', 'medium', 'high', 'max', 'xhigh']

    expect(gpt55Values).toEqual(['none', 'low', 'medium', 'high', 'xhigh'])
    expect(chatReasoningEffortOptionsForModel('gpt-5.6').map((option) => option.value)).toEqual(expected)
    expect(chatReasoningEffortOptionsForModel('GPT-5.6-sol').map((option) => option.value)).toEqual(expected)
    expect(chatReasoningEffortOptionsForModel('openai/gpt-5.6-terra').map((option) => option.value)).toEqual(expected)
  })

  it('drops obsolete GPT-5.6-only saved values', () => {
    expect(normalizeChatReasoningEffort('auto')).toBe('')
    expect(normalizeChatReasoningEffort('ultra')).toBe('')
    expect(normalizeChatReasoningEffort('x-high')).toBe('xhigh')
  })
})
