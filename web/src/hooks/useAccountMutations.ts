import { useState } from 'react'
import { errorMessage, request } from '../api'
import type { ProviderAccount } from '../types'

export function useAccountMutations(reload: () => Promise<void>) {
  const [busyID, setBusyID] = useState('')
  const [error, setError] = useState('')

  async function refresh(accountID: string, afterRefresh?: () => Promise<void>) {
    setBusyID(accountID)
    setError('')
    try {
      await request(`/api/v1/provider-accounts/${accountID}/refresh`, { method: 'POST' })
      await reload()
      await afterRefresh?.()
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setBusyID('')
    }
  }

  async function updateStatus(account: ProviderAccount, status: 'active' | 'disabled') {
    setBusyID(account.id)
    setError('')
    try {
      await request(`/api/v1/provider-accounts/${account.id}`, { method: 'PUT', body: JSON.stringify({ display_name: account.display_name, status }) })
      await reload()
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setBusyID('')
    }
  }

  async function remove(account: ProviderAccount) {
    setBusyID(account.id)
    setError('')
    try {
      await request(`/api/v1/provider-accounts/${account.id}`, { method: 'DELETE' })
      await reload()
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setBusyID('')
    }
  }

  return { busyID, error, refresh, updateStatus, remove }
}
