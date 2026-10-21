import { useEffect, useRef, useState } from 'react'
import { errorMessage, request } from '../../api'

interface GoogleAuthorization {
  login_id: string
  authorization_url: string
  expires_at: string
}

export function useAntigravityLogin(onConnected: () => Promise<void>) {
  const [login, setLogin] = useState<GoogleAuthorization | null>(null)
  const [callbackURL, setCallbackURL] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const active = useRef<AbortController | null>(null)
  useEffect(() => () => active.current?.abort(), [])

  function cancel() {
    active.current?.abort()
    active.current = null
    setLogin(null)
    setCallbackURL('')
    setError('')
    setBusy(false)
  }

  async function start() {
    cancel()
    const controller = new AbortController()
    active.current = controller
    setBusy(true)
    try {
      const authorization = await request<GoogleAuthorization>('/api/v1/provider-accounts/antigravity/oauth', { method: 'POST', signal: controller.signal })
      if (!controller.signal.aborted) setLogin(authorization)
    } catch (caught) {
      if (!controller.signal.aborted) setError(errorMessage(caught))
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  async function complete(displayName: string) {
    if (!login || busy) return
    if (!callbackURL.trim()) { setError('Paste the complete Google callback URL.'); return }
    if (Date.parse(login.expires_at) <= Date.now()) { setError('This authorization expired. Start again.'); return }
    const controller = new AbortController()
    active.current = controller
    setBusy(true)
    setError('')
    try {
      await request('/api/v1/provider-accounts/antigravity/oauth/complete', { method: 'POST', signal: controller.signal,
        body: JSON.stringify({ login_id: login.login_id, callback_url: callbackURL.trim(), display_name: displayName }) })
      if (!controller.signal.aborted) {
        setCallbackURL('')
        await onConnected()
      }
    } catch (caught) {
      if (!controller.signal.aborted) setError(errorMessage(caught))
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  return { login, callbackURL, setCallbackURL, busy, error, start, complete, cancel }
}
