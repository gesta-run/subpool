import { useCallback, useEffect, useState } from 'react'
import { collection, errorMessage, request } from '../api'

export function useRemoteList<T>(path: string, keys: string[]) {
  const [items, setItems] = useState<T[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const reload = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const payload = await request<unknown>(path)
      setItems(collection<T>(payload, keys))
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setLoading(false)
    }
  }, [path, keys.join('|')]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void reload()
  }, [reload])

  return { items, setItems, loading, error, reload }
}
