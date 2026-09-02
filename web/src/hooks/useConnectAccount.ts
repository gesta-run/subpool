import { useState } from 'react'
import { errorMessage, request } from '../api'

export type ConnectProvider = 'codex' | 'openai_compatible'

function endpointErrors(displayName: string, baseURL: string, apiKey: string) {
  const errors: Record<string, string> = {}
  if (!displayName.trim()) errors.displayName = 'Enter a display name.'
  if (!baseURL.trim()) errors.baseURL = 'Enter the OpenAI-compatible Base URL.'
  else {
    try {
      const parsed = new URL(baseURL)
      if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') errors.baseURL = 'Use an HTTP or HTTPS URL.'
    } catch {
      errors.baseURL = 'Enter a valid URL.'
    }
  }
  if (!apiKey.trim()) errors.apiKey = 'Enter the upstream API key.'
  return errors
}

export function useConnectAccount(reload: () => Promise<void>) {
  const [open, setOpen] = useState(false)
  const [displayName, setDisplayName] = useState('')
  const [provider, setProvider] = useState<ConnectProvider>('codex')
  const [baseURL, setBaseURL] = useState('')
  const [apiKey, setAPIKey] = useState('')
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  function close() {
    setOpen(false)
    setProvider('codex')
    setDisplayName('')
    setBaseURL('')
    setAPIKey('')
    setFieldErrors({})
    setError('')
  }

  async function submit() {
    if (!displayName.trim()) {
      setFieldErrors({ displayName: 'Enter a display name.' })
      return
    }
    if (provider === 'openai_compatible') {
      const errors = endpointErrors(displayName, baseURL, apiKey)
      setFieldErrors(errors)
      if (Object.keys(errors).length > 0) return
    }
    setError('')
    setBusy(true)
    try {
      if (provider === 'codex') {
        const result = await request<{ authorization_url?: string; url?: string }>('/api/v1/provider-accounts/oauth/start', { method: 'POST', body: JSON.stringify({ display_name: displayName.trim() }) })
        const url = result.authorization_url ?? result.url
        if (!url) throw new Error('The server did not return an authorization URL.')
        window.location.assign(url)
        return
      }
      await request('/api/v1/provider-accounts', { method: 'POST', body: JSON.stringify({ provider, display_name: displayName.trim(), base_url: baseURL.trim(), api_key: apiKey.trim() }) })
      close()
      await reload()
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setBusy(false)
    }
  }

  function changeProvider(value: ConnectProvider) {
    setProvider(value)
    setFieldErrors({})
    setError('')
  }

  return { open, setOpen, displayName, setDisplayName, provider, changeProvider, baseURL, setBaseURL, apiKey, setAPIKey, fieldErrors, setFieldErrors, error, busy, close, submit }
}
