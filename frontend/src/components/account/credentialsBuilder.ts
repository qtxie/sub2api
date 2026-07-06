export function applyInterceptWarmup(
  credentials: Record<string, unknown>,
  enabled: boolean,
  mode: 'create' | 'edit'
): void {
  if (enabled) {
    credentials.intercept_warmup_requests = true
  } else if (mode === 'edit') {
    delete credentials.intercept_warmup_requests
  }
}

export const ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY = 'antigravity_project_id'
export const OPENAI_USER_AGENT_CREDENTIAL_KEY = 'user_agent'

export function applyAntigravityProjectID(
  credentials: Record<string, unknown>,
  projectId: string,
  mode: 'create' | 'edit'
): void {
  const trimmed = projectId.trim()
  if (trimmed) {
    credentials[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY] = trimmed
  } else if (mode === 'edit') {
    delete credentials[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]
  }
}

export function applyOpenAIUserAgent(
  credentials: Record<string, unknown>,
  userAgent: string,
  mode: 'create' | 'edit'
): void {
  const trimmed = userAgent.trim()
  if (trimmed) {
    credentials[OPENAI_USER_AGENT_CREDENTIAL_KEY] = trimmed
  } else if (mode === 'edit') {
    delete credentials[OPENAI_USER_AGENT_CREDENTIAL_KEY]
  }
}
