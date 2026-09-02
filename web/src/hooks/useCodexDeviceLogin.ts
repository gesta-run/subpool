import { useEffect, useRef, useState } from 'react'
import { errorMessage, request } from '../api'
import { copyText } from '../clipboard'

export interface CodexDeviceLogin {
  login_id: string
  user_code: string
  verification_url: string
  expires_at: string
}

interface DeviceLoginStatus {
  status: 'pending' | 'completed' | 'failed'
  message?: string
}

const retryDelay = (failures: number) => Math.min(1500 * (2 ** Math.max(0, failures - 1)), 10_000)

export function useCodexDeviceLogin(onCompleted: () => Promise<void>) {
  const [login, setLogin] = useState<CodexDeviceLogin | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [copyStatus, setCopyStatus] = useState('')
  const completedRef = useRef(onCompleted)
  completedRef.current = onCompleted

  useEffect(() => {
    if (!login) return
    let stopped = false
    let failures = 0
    let timer: number | undefined
    const cancel = () => request(`/api/v1/provider-accounts/codex/device-login/${encodeURIComponent(login.login_id)}`, { method: 'DELETE' }).catch(() => undefined)
    const poll = async () => {
      try {
        const result = await request<DeviceLoginStatus>(`/api/v1/provider-accounts/codex/device-login/${encodeURIComponent(login.login_id)}`)
        if (stopped) return
        failures = 0
        setError('')
        if (result.status === 'completed') {
          setLogin(null)
          setCopyStatus('')
          await completedRef.current()
          return
        }
        if (result.status === 'failed') {
          setLogin(null)
          setError(result.message || 'Codex authorization failed. Start again.')
          return
        }
        timer = window.setTimeout(poll, 1500)
      } catch (caught) {
        if (stopped) return
        if (Date.now() >= Date.parse(login.expires_at)) {
          setLogin(null)
          setError('Authorization status could not be confirmed before the code expired.')
          void cancel()
          return
        }
        failures++
        setError(`Connection interrupted. Retrying… ${errorMessage(caught)}`)
        timer = window.setTimeout(poll, retryDelay(failures))
      }
    }
    timer = window.setTimeout(poll, 1000)
    return () => {
      stopped = true
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [login])

  async function start(displayName: string) {
    setError('')
    setBusy(true)
    try {
      const result = await request<CodexDeviceLogin>('/api/v1/provider-accounts/codex/device-login', { method: 'POST', body: JSON.stringify({ display_name: displayName }) })
      if (!result.login_id || !result.user_code || !result.verification_url || !Date.parse(result.expires_at)) {
        throw new Error('The server returned an invalid device authorization response.')
      }
      setLogin(result)
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setBusy(false)
    }
  }

  function cancel() {
    const loginID = login?.login_id
    setLogin(null)
    setError('')
    setCopyStatus('')
    if (loginID) void request(`/api/v1/provider-accounts/codex/device-login/${encodeURIComponent(loginID)}`, { method: 'DELETE' }).catch(() => undefined)
  }

  async function continueToOpenAI() {
    if (!login) return
    window.open(login.verification_url, '_blank', 'noopener,noreferrer')
    try {
      await copyText(login.user_code)
      setCopyStatus('Code copied')
    } catch {
      setCopyStatus('Copy the code shown above')
    }
  }

  return { login, error, busy, copyStatus, setError, start, cancel, continueToOpenAI }
}
