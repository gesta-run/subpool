import { type FormEvent, useEffect, useState } from 'react'
import { errorMessage, request } from '../api'
import { PageSkeleton } from '../components/PageSkeleton'
import { StatePanel } from '../components/StatePanel'
import { Spinner } from '../components/Spinner'
import type { GlobalSettings } from '../types'

export function SettingsPage() {
  const [keyLimit, setKeyLimit] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  async function load() {
    setLoading(true)
    setError('')
    try {
      const settings = await request<GlobalSettings>('/api/v1/settings')
      setKeyLimit(String(settings.max_api_keys_per_account))
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { void load() }, [])

  async function save(event: FormEvent) {
    event.preventDefault()
    const maxKeys = Number(keyLimit)
    if (!Number.isInteger(maxKeys) || maxKeys < 1 || maxKeys > 100) {
      setError('Enter a whole number between 1 and 100.')
      return
    }
    setSaving(true)
    setSaved(false)
    setError('')
    try {
      const settings = await request<GlobalSettings>('/api/v1/settings', {
        method: 'PUT',
        body: JSON.stringify({ max_api_keys_per_account: maxKeys }),
      })
      setKeyLimit(String(settings.max_api_keys_per_account))
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
          <h2 id="settings-heading">Global settings</h2>
          <p>Configure rules that apply consistently to every connected upstream account.</p>
        </div>
      </header>
      {loading ? <PageSkeleton variant="settings" /> : error && keyLimit === '' ? <StatePanel kind="error" title="Settings unavailable" description={error} actionLabel="Try again" onAction={() => void load()} /> : (
        <form className="settings-panel form-stack" onSubmit={(event) => void save(event)}>
          <div>
            <p className="eyebrow">Request routing</p>
            <h3>Employee capacity</h3>
            <p>Limit how many active employee API keys can be assigned to each upstream account.</p>
          </div>
          <div className="field">
            <label htmlFor="global-max-api-keys">Maximum API keys per account</label>
            <input id="global-max-api-keys" type="number" min="1" max="100" step="1" value={keyLimit} onChange={(event) => { setKeyLimit(event.target.value); setSaved(false) }} aria-describedby="global-max-api-keys-help" aria-invalid={error ? true : undefined} />
            <small id="global-max-api-keys-help">New employee keys are assigned only to accounts with an available key slot.</small>
          </div>
          {error ? <div className="inline-alert" role="alert">{error}</div> : null}
          {saved ? <p className="save-confirmation" role="status">Global routing setting saved.</p> : null}
          <div className="form-actions"><button className="button button--primary" type="submit" disabled={saving}>{saving ? <><Spinner /> Saving…</> : 'Save setting'}</button></div>
        </form>
      )}
    </section>
  )
}
