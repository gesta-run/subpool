import { useCallback, useEffect, useRef, useState } from 'react'
import { errorMessage, request } from '../api'
import type { UsagePageResponse } from '../types'

export function useUsagePage(path: string) {
  const [data, setData] = useState<UsagePageResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const requestID = useRef(0)
  const activeRequest = useRef<AbortController | null>(null)

  const reload = useCallback(async () => {
    const currentRequestID = requestID.current + 1
    requestID.current = currentRequestID
    activeRequest.current?.abort()
    const controller = new AbortController()
    activeRequest.current = controller
    setLoading(true)
    setError('')
    try {
      const payload = await request<UsagePageResponse>(path, { signal: controller.signal })
      if (requestID.current !== currentRequestID) return
      setData(payload)
    } catch (caught) {
      if (controller.signal.aborted || requestID.current !== currentRequestID) return
      setError(errorMessage(caught))
    } finally {
      if (requestID.current !== currentRequestID) return
      activeRequest.current = null
      setLoading(false)
    }
  }, [path])

  useEffect(() => {
    void reload()
    return () => {
      requestID.current += 1
      activeRequest.current?.abort()
      activeRequest.current = null
    }
  }, [reload])

  return { data, loading, error, reload }
}
