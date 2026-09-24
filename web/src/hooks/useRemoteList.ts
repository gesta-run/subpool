import { useCallback, useEffect, useRef, useState } from 'react'
import { collection, errorMessage, request } from '../api'

export function useRemoteList<T>(path: string, keys: string[]) {
  const [items, setItems] = useState<T[]>([])
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
      const payload = await request<unknown>(path, { signal: controller.signal })
      if (requestID.current !== currentRequestID) return
      setItems(collection<T>(payload, keys))
    } catch (caught) {
      if (controller.signal.aborted || requestID.current !== currentRequestID) return
      setError(errorMessage(caught))
    } finally {
      if (requestID.current !== currentRequestID) return
      activeRequest.current = null
      setLoading(false)
    }
  }, [path, keys.join('|')]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void reload()
    return () => {
      requestID.current += 1
      activeRequest.current?.abort()
      activeRequest.current = null
    }
  }, [reload])

  return { items, setItems, loading, error, reload }
}
