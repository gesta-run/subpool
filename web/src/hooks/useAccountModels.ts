import { useRef, useState } from 'react'
import { errorMessage, request } from '../api'
import type { ProviderAccount, ProviderModel } from '../types'

export function useAccountModels() {
  const [account, setAccount] = useState<ProviderAccount | null>(null)
  const [models, setModels] = useState<ProviderModel[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const requestID = useRef(0)

  async function load(target: ProviderAccount) {
    const currentRequest = ++requestID.current
    setModels([])
    setError('')
    setLoading(true)
    try {
      const result = await request<ProviderModel[]>(`/api/v1/provider-accounts/${target.id}/models`)
      if (requestID.current === currentRequest) setModels(result)
    } catch (caught) {
      if (requestID.current === currentRequest) setError(errorMessage(caught))
    } finally {
      if (requestID.current === currentRequest) setLoading(false)
    }
  }

  function open(target: ProviderAccount) {
    setAccount(target)
    void load(target)
  }

  function close() {
    requestID.current += 1
    setAccount(null)
    setModels([])
    setError('')
    setLoading(false)
  }

  return { account, models, loading, error, open, close, reload: () => account ? load(account) : Promise.resolve() }
}
