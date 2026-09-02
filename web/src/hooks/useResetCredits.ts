import { useRef, useState } from 'react'
import { errorMessage, request } from '../api'
import type { CodexResetConsumeResponse, CodexResetCredits, CodexResetCreditsResponse, ProviderAccount } from '../types'

export interface ResetCreditState {
  loading: boolean
  data?: CodexResetCredits | null
  error?: string
  notice?: string
}

export function useResetCredits(reload: () => Promise<void>) {
  const [states, setStates] = useState<Record<string, ResetCreditState>>({})
  const [busyID, setBusyID] = useState('')
  const loaded = useRef(new Set<string>())

  async function load(accountID: string, force = false) {
    if (!force && loaded.current.has(accountID)) return
    loaded.current.add(accountID)
    setStates((current) => ({ ...current, [accountID]: { ...current[accountID], loading: true, error: undefined } }))
    try {
      const suffix = force ? '?refresh=true' : ''
      const result = await request<CodexResetCreditsResponse>(`/api/v1/provider-accounts/${accountID}/reset-credits${suffix}`)
      setStates((current) => ({ ...current, [accountID]: { loading: false, data: result.reset_credits } }))
    } catch (caught) {
      setStates((current) => ({ ...current, [accountID]: { loading: false, error: errorMessage(caught) } }))
    }
  }

  async function consume(account: ProviderAccount, creditID?: string) {
    setBusyID(account.id)
    try {
      const result = await request<CodexResetConsumeResponse>(`/api/v1/provider-accounts/${account.id}/reset-credits/consume`, {
        method: 'POST', body: JSON.stringify({ credit_id: creditID, idempotency_key: crypto.randomUUID() }),
      })
      const notice = result.outcome === 'reset' || result.outcome === 'alreadyRedeemed'
        ? 'Full reset applied. Quota has been refreshed.'
        : result.outcome === 'nothingToReset' ? 'No rate-limit window is currently eligible for reset.' : 'No reset credits are available.'
      setStates((current) => ({ ...current, [account.id]: { loading: false, data: result.reset_credits, notice } }))
      await reload()
    } catch (caught) {
      setStates((current) => ({ ...current, [account.id]: { ...current[account.id], loading: false, error: errorMessage(caught) } }))
    } finally {
      setBusyID('')
    }
  }

  return { states, busyID, load, consume }
}
