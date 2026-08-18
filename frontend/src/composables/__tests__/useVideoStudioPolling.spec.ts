import { defineComponent, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useVideoStudioPolling } from '../useVideoStudioPolling'
import type { VideoStudioStatusResponse } from '@/api/videoStudio'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function mountPolling(poll: ReturnType<typeof vi.fn>, onResult = vi.fn(), onError = vi.fn()) {
  let controls!: ReturnType<typeof useVideoStudioPolling>
  const wrapper = mount(defineComponent({
    setup() {
      controls = useVideoStudioPolling({
        poll,
        onResult,
        onError,
        baseDelayMs: 100,
        maxDelayMs: 1000,
        jitterRatio: 0
      })
      return () => null
    }
  }))
  return { wrapper, controls, onResult, onError }
}

async function flushPromises() {
  await Promise.resolve()
  await Promise.resolve()
  await nextTick()
}

describe('useVideoStudioPolling', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('never overlaps polls and stops after a terminal result', async () => {
    const first = deferred<VideoStudioStatusResponse>()
    const poll = vi.fn()
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce({ request_id: 'task-1', status: 'done' })
    const { wrapper, controls, onResult } = mountPolling(poll)

    controls.track({ requestId: 'task-1', apiKeyId: 7 }, true)
    await vi.advanceTimersByTimeAsync(0)
    expect(poll).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(500)
    expect(poll).toHaveBeenCalledTimes(1)

    first.resolve({ request_id: 'task-1', status: 'pending' })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(99)
    expect(poll).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(poll).toHaveBeenCalledTimes(2)
    expect(onResult).toHaveBeenCalledTimes(2)
    expect(controls.isTracking('task-1')).toBe(false)

    await vi.advanceTimersByTimeAsync(1000)
    expect(poll).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('backs off after errors before retrying', async () => {
    const poll = vi.fn()
      .mockRejectedValueOnce(new Error('temporary'))
      .mockResolvedValueOnce({ request_id: 'task-2', status: 'done' })
    const { wrapper, controls, onError } = mountPolling(poll)

    controls.track({ requestId: 'task-2', apiKeyId: 7 }, true)
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    expect(onError).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(199)
    expect(poll).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(poll).toHaveBeenCalledTimes(2)
    expect(controls.isTracking('task-2')).toBe(false)
    wrapper.unmount()
  })

  it('does not let a stopped in-flight state schedule after the same ID is retracked', async () => {
    const oldPoll = deferred<VideoStudioStatusResponse>()
    const poll = vi.fn()
      .mockReturnValueOnce(oldPoll.promise)
      .mockResolvedValue({ request_id: 'task-3', status: 'done' })
    const { wrapper, controls } = mountPolling(poll)

    controls.track({ requestId: 'task-3', apiKeyId: 7 }, true)
    await vi.advanceTimersByTimeAsync(0)
    expect(poll).toHaveBeenCalledTimes(1)
    controls.stop('task-3')
    controls.track({ requestId: 'task-3', apiKeyId: 7 }, true)
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    expect(poll).toHaveBeenCalledTimes(2)

    oldPoll.resolve({ request_id: 'task-3', status: 'pending' })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(1000)
    expect(poll).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('aborts while hidden and resumes immediately when visible', async () => {
    let visibility: DocumentVisibilityState = 'visible'
    vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility)
    const poll = vi.fn()
      .mockImplementationOnce((_target, signal: AbortSignal) => new Promise((_resolve, reject) => {
        signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
      }))
      .mockResolvedValueOnce({ request_id: 'task-4', status: 'done' })
    const { wrapper, controls } = mountPolling(poll)

    controls.track({ requestId: 'task-4', apiKeyId: 7 }, true)
    await vi.advanceTimersByTimeAsync(0)
    const firstSignal = poll.mock.calls[0][1] as AbortSignal
    expect(firstSignal.aborted).toBe(false)

    visibility = 'hidden'
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(firstSignal.aborted).toBe(true)
    await vi.advanceTimersByTimeAsync(1000)
    expect(poll).toHaveBeenCalledTimes(1)

    visibility = 'visible'
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(0)
    await flushPromises()
    expect(poll).toHaveBeenCalledTimes(2)
    expect(controls.isTracking('task-4')).toBe(false)
    wrapper.unmount()
  })
})
