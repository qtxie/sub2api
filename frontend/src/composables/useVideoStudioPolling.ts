import { onBeforeUnmount, onMounted } from 'vue'
import type { VideoStudioStatusResponse } from '@/api/videoStudio'

export interface VideoStudioPollingTarget {
  requestId: string
  apiKeyId: number
}

interface VideoStudioPollingOptions {
  poll: (target: VideoStudioPollingTarget, signal: AbortSignal) => Promise<VideoStudioStatusResponse>
  onResult: (target: VideoStudioPollingTarget, result: VideoStudioStatusResponse) => void | Promise<void>
  onError?: (target: VideoStudioPollingTarget, error: unknown) => void | Promise<void>
  baseDelayMs?: number
  maxDelayMs?: number
  jitterRatio?: number
}

interface PollState {
  target: VideoStudioPollingTarget
  paused: boolean
  failures: number
  timer: ReturnType<typeof setTimeout> | null
  controller: AbortController | null
  running: boolean
}

export function useVideoStudioPolling(options: VideoStudioPollingOptions) {
  const baseDelayMs = Math.max(100, options.baseDelayMs ?? 5000)
  const maxDelayMs = Math.max(baseDelayMs, options.maxDelayMs ?? 30000)
  const jitterRatio = Math.min(0.5, Math.max(0, options.jitterRatio ?? 0.15))
  const states = new Map<string, PollState>()
  let active = false

  function track(target: VideoStudioPollingTarget, immediate = false) {
    const requestId = target.requestId.trim()
    if (!requestId || target.apiKeyId <= 0) return
    const existing = states.get(requestId)
    if (existing) {
      existing.target = { ...target, requestId }
      existing.paused = false
      if (active && !existing.running && !existing.timer && isDocumentVisible()) {
        schedule(existing, immediate ? 0 : baseDelayMs)
      }
      return
    }
    const state: PollState = {
      target: { ...target, requestId },
      paused: false,
      failures: 0,
      timer: null,
      controller: null,
      running: false
    }
    states.set(requestId, state)
    if (active && isDocumentVisible()) schedule(state, immediate ? 0 : baseDelayMs)
  }

  function pause(requestId: string) {
    const state = states.get(requestId)
    if (!state) return
    state.paused = true
    clearStateTimer(state)
    state.controller?.abort()
  }

  function resume(requestId: string) {
    const state = states.get(requestId)
    if (!state) return
    state.paused = false
    state.failures = 0
    if (active && !state.running && isDocumentVisible()) schedule(state, 0)
  }

  function stop(requestId: string) {
    const state = states.get(requestId)
    if (!state) return
    clearStateTimer(state)
    state.controller?.abort()
    states.delete(requestId)
  }

  function stopAll() {
    for (const requestId of [...states.keys()]) stop(requestId)
  }

  function isTracking(requestId: string): boolean {
    const state = states.get(requestId)
    return Boolean(state && !state.paused)
  }

  function start() {
    if (active) return
    active = true
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', handleVisibilityChange)
    if (!isDocumentVisible()) return
    for (const state of states.values()) {
      if (!state.paused && !state.running && !state.timer) schedule(state, 0)
    }
  }

  function dispose() {
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', handleVisibilityChange)
    active = false
    stopAll()
  }

  function schedule(state: PollState, delayMs: number) {
    if (!active || state.paused || state.running || state.timer || !ownsState(state)) return
    if (!isDocumentVisible()) return
    const jitteredDelay = delayMs > 0
      ? Math.round(delayMs * (1 - jitterRatio + Math.random() * jitterRatio * 2))
      : 0
    state.timer = setTimeout(() => {
      state.timer = null
      void run(state)
    }, Math.max(0, jitteredDelay))
  }

  async function run(state: PollState) {
    if (!active || state.paused || state.running || !ownsState(state) || !isDocumentVisible()) return
    state.running = true
    const controller = new AbortController()
    state.controller = controller
    try {
      const result = await options.poll(state.target, controller.signal)
      if (controller.signal.aborted || !ownsState(state)) return
      state.failures = 0
      const terminal = result.status !== 'pending'
      try {
        await options.onResult(state.target, result)
      } catch (error) {
        await notifyError(state.target, error)
      }
      if (terminal) {
        if (ownsState(state)) stop(state.target.requestId)
        return
      }
      schedule(state, baseDelayMs)
    } catch (error) {
      if (controller.signal.aborted || !ownsState(state)) return
      state.failures += 1
      await notifyError(state.target, error)
      const delay = Math.min(maxDelayMs, baseDelayMs * (2 ** Math.min(state.failures, 4)))
      schedule(state, delay)
    } finally {
      if (state.controller === controller) state.controller = null
      state.running = false
      if (active && !state.paused && !state.timer && ownsState(state) && isDocumentVisible()) {
        schedule(state, state.failures > 0
          ? Math.min(maxDelayMs, baseDelayMs * (2 ** Math.min(state.failures, 4)))
          : baseDelayMs)
      }
    }
  }

  async function notifyError(target: VideoStudioPollingTarget, error: unknown) {
    try {
      await options.onError?.(target, error)
    } catch {
      // Polling must continue even if an error observer fails.
    }
  }

  function handleVisibilityChange() {
    if (!isDocumentVisible()) {
      for (const state of states.values()) {
        clearStateTimer(state)
        state.controller?.abort()
      }
      return
    }
    for (const state of states.values()) {
      if (!state.paused && !state.running && !state.timer) schedule(state, 0)
    }
  }

  function clearStateTimer(state: PollState) {
    if (state.timer) clearTimeout(state.timer)
    state.timer = null
  }

  function ownsState(state: PollState): boolean {
    return states.get(state.target.requestId) === state
  }

  function isDocumentVisible(): boolean {
    return typeof document === 'undefined' || document.visibilityState !== 'hidden'
  }

  onMounted(start)
  onBeforeUnmount(dispose)

  return { track, pause, resume, stop, stopAll, isTracking, start, dispose }
}

export default useVideoStudioPolling
