import { renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useRemoteList } from './useRemoteList'

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { 'content-type': 'application/json' },
  })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise })
  return { promise, resolve }
}

describe('useRemoteList', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn())
  })

  it('ignores a superseded response that completes after the current request', async () => {
    const slow = deferred<Response>()
    const fast = deferred<Response>()
    vi.mocked(fetch).mockImplementation((input) => String(input) === '/slow' ? slow.promise : fast.promise)

    const { result, rerender } = renderHook(({ path }) => useRemoteList<{ id: string }>(path, ['records']), {
      initialProps: { path: '/slow' },
    })
    const slowSignal = vi.mocked(fetch).mock.calls[0][1]?.signal

    rerender({ path: '/fast' })
    expect(slowSignal?.aborted).toBe(true)

    fast.resolve(json({ records: [{ id: 'current' }] }))
    await waitFor(() => expect(result.current.items).toEqual([{ id: 'current' }]))

    slow.resolve(json({ records: [{ id: 'stale' }] }))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.items).toEqual([{ id: 'current' }])
    expect(result.current.error).toBe('')
  })
})
