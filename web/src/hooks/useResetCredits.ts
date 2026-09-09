import { useRef, useState } from 'react'
import { errorMessage, request } from '../api'
import type { CodexResetConsumeResponse, CodexResetCredits, CodexResetCreditsResponse, ProviderAccount } from '../types'

export interface ResetCreditState {
  loading: boolean
  data?: CodexResetCredits | null
  error?: string
  notice?: string
}

export function createIdempotencyKey(
  randomUUID: (() => string) | undefined = typeof crypto.randomUUID === 'function' ? () => crypto.randomUUID() : undefined,
  fillRandom: (values: Uint8Array<ArrayBuffer>) => void = (values) => { crypto.getRandomValues(values) },
) {
  if (randomUUID) return randomUUID()
  const bytes = new Uint8Array(16)
  fillRandom(bytes)
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
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
        method: 'POST', body: JSON.stringify({ credit_id: creditID, idempotency_key: createIdempotencyKey() }),
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
