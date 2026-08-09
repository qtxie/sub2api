export function parseOpenAIAPIKeyListInput(input: string, primaryKey = ''): string[] {
  const primary = primaryKey.trim()
  const seen = new Set<string>()
  if (primary) seen.add(primary)

  const result: string[] = []
  for (const line of input.split(/\r?\n/)) {
    const key = line.trim()
    if (!key || seen.has(key)) continue
    seen.add(key)
    result.push(key)
  }
  return result
}
