import { useState } from 'react'
import { errorMessage, request } from '../api'
import { useDeviceLogin } from './useDeviceLogin'
export type { DeviceLogin } from './useDeviceLogin'

export type ConnectProvider = 'codex' | 'copilot' | 'openai_compatible'

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
  const device = useDeviceLogin(async () => {
    setOpen(false)
    setDisplayName('')
    await reload()
  })

  function close() {
    setOpen(false)
    setProvider('codex')
    setDisplayName('')
    setBaseURL('')
    setAPIKey('')
    setFieldErrors({})
    setError('')
    device.cancel()
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
    device.setError('')
    try {
      if (provider !== 'openai_compatible') {
        await device.start(displayName.trim(), provider)
        return
      }
      setBusy(true)
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
    device.setError('')
  }

  return { open, setOpen, displayName, setDisplayName, provider, changeProvider, baseURL, setBaseURL, apiKey, setAPIKey, fieldErrors, setFieldErrors, error: device.error || error, busy: device.busy || busy, deviceLogin: device.login, copyStatus: device.copyStatus, continueToProvider: device.continueToProvider, close, submit }
}
