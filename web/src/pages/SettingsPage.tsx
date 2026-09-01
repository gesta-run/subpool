import { type FormEvent, useEffect, useState } from 'react'
import { errorMessage, request } from '../api'
import { StatePanel } from '../components/StatePanel'
import type { GlobalSettings } from '../types'

export function SettingsPage() {
  const [value, setValue] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  async function load() {
    setLoading(true)
    setError('')
    try {
      const settings = await request<GlobalSettings>('/api/v1/settings')
      setValue(String(settings.max_api_keys_per_account))
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { void load() }, [])

  async function save(event: FormEvent) {
    event.preventDefault()
    const limit = Number(value)
    if (!Number.isInteger(limit) || limit < 1 || limit > 100) {
      setError('Enter a whole number between 1 and 100.')
      return
    }
    setSaving(true)
    setSaved(false)
    setError('')
    try {
      const settings = await request<GlobalSettings>('/api/v1/settings', {
        method: 'PUT',
        body: JSON.stringify({ max_api_keys_per_account: limit }),
      })
      setValue(String(settings.max_api_keys_per_account))
      setSaved(true)
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setSaving(false)
    }
  }

  return (
    <section aria-labelledby="settings-heading">
      <header className="page-heading">
        <div>
          <p className="eyebrow">Routing policy</p>
          <h2 id="settings-heading">Global settings</h2>
          <p>Configure rules that apply consistently to every connected upstream account.</p>
        </div>
      </header>
      {loading ? <StatePanel kind="loading" title="Loading settings" description="Reading the current routing policy." /> : error && value === '' ? <StatePanel kind="error" title="Settings unavailable" description={error} actionLabel="Try again" onAction={() => void load()} /> : (
        <form className="settings-panel form-stack" onSubmit={(event) => void save(event)}>
          <div>
            <p className="eyebrow">Account assignment</p>
            <h3>API key capacity</h3>
            <p>Each upstream account can be assigned to at most this many employee API keys.</p>
          </div>
          <div className="field">
            <label htmlFor="global-max-keys">Maximum API keys per account</label>
            <input id="global-max-keys" type="number" min="1" max="100" step="1" value={value} onChange={(event) => { setValue(event.target.value); setSaved(false) }} aria-describedby="global-max-keys-help" aria-invalid={error ? true : undefined} />
            <small id="global-max-keys-help">This is a global routing limit, not a request concurrency limit. Lowering it below an account's current assignments is blocked.</small>
          </div>
          {error ? <div className="inline-alert" role="alert">{error}</div> : null}
          {saved ? <p className="save-confirmation" role="status">Global routing setting saved.</p> : null}
          <div className="form-actions"><button className="button button--primary" type="submit" disabled={saving}>{saving ? 'Saving…' : 'Save setting'}</button></div>
        </form>
      )}
    </section>
  )
}
