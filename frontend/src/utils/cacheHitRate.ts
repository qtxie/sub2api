const nonNegativeFinite = (value: number): number =>
  Number.isFinite(value) ? Math.max(0, value) : 0

export const calculateCacheHitRate = (
  inputTokens: number,
  cacheCreationTokens: number,
  cacheReadTokens: number
): number => {
  const input = nonNegativeFinite(inputTokens)
  const cacheCreation = nonNegativeFinite(cacheCreationTokens)
  const cacheRead = nonNegativeFinite(cacheReadTokens)
  const totalPromptTokens = input + cacheCreation + cacheRead

  return totalPromptTokens > 0 ? (cacheRead / totalPromptTokens) * 100 : 0
}

export const formatCacheHitRate = (
  inputTokens: number,
  cacheCreationTokens: number,
  cacheReadTokens: number
): string => `${calculateCacheHitRate(inputTokens, cacheCreationTokens, cacheReadTokens).toFixed(1)}%`
