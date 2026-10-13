import { type FormEvent, useEffect, useState } from 'react'
import { errorMessage, request } from '../api'
import { PageSkeleton } from '../components/PageSkeleton'
import { StatePanel } from '../components/StatePanel'
import { Spinner } from '../components/Spinner'
import type { GlobalSettings } from '../types'

const MiB = 1024 * 1024
type Field = 'keyLimit' | 'bodyLimit' | 'bufferLimit'
const fieldIDs: Record<Field, string> = {
  keyLimit: 'global-max-api-keys', bodyLimit: 'global-max-request-body', bufferLimit: 'global-max-inflight-body',
}

function validate(keyLimit: string, bodyLimit: string, bufferLimit: string): Partial<Record<Field, string>> {
  const errors: Partial<Record<Field, string>> = {}
  const keys = Number(keyLimit)
  const body = Number(bodyLimit)
  const buffer = Number(bufferLimit)
  if (!Number.isInteger(keys) || keys < 1 || keys > 100) errors.keyLimit = 'Enter a whole number between 1 and 100.'
  if (body <= 0 || !Number.isSafeInteger(body * MiB)) errors.bodyLimit = 'Enter a positive MiB value that represents a whole number of bytes.'
  if (buffer <= 0 || !Number.isSafeInteger(buffer * MiB)) {
    errors.bufferLimit = 'Enter a positive MiB value that represents a whole number of bytes.'
  } else if (!errors.bodyLimit && buffer < body * 4) {
    errors.bufferLimit = 'The buffer budget must be at least four times the request body limit.'
  }
  return errors
}

function useSettingsForm() {
  const [keyLimit, setKeyLimit] = useState('')
  const [bodyLimit, setBodyLimit] = useState('')
  const [bufferLimit, setBufferLimit] = useState('')
  const [fieldErrors, setFieldErrors] = useState<Partial<Record<Field, string>>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [persistedSettings, setPersistedSettings] = useState<GlobalSettings | null>(null)

  function applySettings(settings: GlobalSettings) {
    setKeyLimit(String(settings.max_api_keys_per_account))
    setBodyLimit(String(settings.max_request_body_bytes / MiB))
    setBufferLimit(String(settings.max_inflight_request_body_bytes / MiB))
    setPersistedSettings(settings)
  }

  async function load() {
    setLoading(true)
    setError('')
    try {
      const settings = await request<GlobalSettings>('/api/v1/settings', { signal: AbortSignal.timeout(15_000) })
      applySettings(settings)
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { void load() }, [])

  function validateField(field: Field) {
    const errors = validate(keyLimit, bodyLimit, bufferLimit)
    setFieldErrors((previous) => ({ ...previous, [field]: errors[field] }))
  }

  function change(field: Field, value: string) {
    if (field === 'keyLimit') setKeyLimit(value)
    if (field === 'bodyLimit') setBodyLimit(value)
    if (field === 'bufferLimit') setBufferLimit(value)
    setSaved(false)
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    const errors = validate(keyLimit, bodyLimit, bufferLimit)
    setFieldErrors(errors)
    const invalid = (Object.keys(fieldIDs) as Field[]).find((field) => errors[field])
    if (invalid) {
      const input = document.getElementById(fieldIDs[invalid])
      input?.focus()
      input?.scrollIntoView?.({ block: 'nearest' })
      return
    }
    setSaving(true)
    setSaved(false)
    setError('')
    try {
      const settings = await request<GlobalSettings>('/api/v1/settings', {
        method: 'PUT',
        signal: AbortSignal.timeout(15_000),
        body: JSON.stringify({
          max_api_keys_per_account: Number(keyLimit),
          ...(Number(bodyLimit) * MiB !== persistedSettings?.max_request_body_bytes
            ? { max_request_body_bytes: Number(bodyLimit) * MiB } : {}),
          ...(Number(bufferLimit) * MiB !== persistedSettings?.max_inflight_request_body_bytes
            ? { max_inflight_request_body_bytes: Number(bufferLimit) * MiB } : {}),
        }),
      })
      applySettings(settings)
      setSaved(true)
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setSaving(false)
    }
  }

  return { keyLimit, bodyLimit, bufferLimit, fieldErrors, loading, saving, error, saved, load, validateField, change, save }
}

export function SettingsPage() {
  const { keyLimit, bodyLimit, bufferLimit, fieldErrors, loading, saving, error, saved, load, validateField, change, save } = useSettingsForm()
  return (
    <section aria-labelledby="settings-heading">
      <header className="page-heading">
        <div>
          <h2 id="settings-heading">Global settings</h2>
          <p>Configure routing capacity and request limits for this deployment.</p>
        </div>
      </header>
      {loading ? <PageSkeleton variant="settings" /> : error && keyLimit === '' ? <StatePanel kind="error" title="Settings unavailable" description={error} actionLabel="Try again" onAction={() => void load()} /> : (
        <form className="settings-panel form-stack" noValidate onSubmit={(event) => void save(event)}>
          <fieldset className="settings-section" disabled={saving}>
            <legend>Employee capacity</legend>
            <p>Limit how many active employee API keys can be assigned to each upstream account.</p>
            <div className="field">
              <label htmlFor="global-max-api-keys">Maximum API keys per account</label>
              <input id="global-max-api-keys" type="number" min="1" max="100" step="1" value={keyLimit} onChange={(event) => change('keyLimit', event.target.value)} onBlur={() => validateField('keyLimit')} aria-describedby={`global-max-api-keys-help${fieldErrors.keyLimit ? ' global-max-api-keys-error' : ''}`} aria-invalid={fieldErrors.keyLimit ? true : undefined} />
              <small id="global-max-api-keys-help">New employee keys are assigned only to accounts with an available key slot.</small>
              {fieldErrors.keyLimit ? <span className="settings-field-error" id="global-max-api-keys-error" role="alert">{fieldErrors.keyLimit}</span> : null}
            </div>
          </fieldset>
          <fieldset className="settings-section" disabled={saving}>
            <legend>Request limits</legend>
            <p>Saved limits apply to new HTTP requests and WebSocket messages without a restart. Requests already being processed keep their original limits.</p>
            <div className="field">
              <label htmlFor="global-max-request-body">Maximum request body (MiB)</label>
              <input id="global-max-request-body" type="number" min={1 / MiB} step="any" value={bodyLimit} onChange={(event) => change('bodyLimit', event.target.value)} onBlur={() => validateField('bodyLimit')} aria-describedby={`global-max-request-body-help${fieldErrors.bodyLimit ? ' global-max-request-body-error' : ''}`} aria-invalid={fieldErrors.bodyLimit ? true : undefined} />
              <small id="global-max-request-body-help">Limits the JSON body accepted by Subpool. This does not increase Copilot or other upstream providers' limits.</small>
              {fieldErrors.bodyLimit ? <span className="settings-field-error" id="global-max-request-body-error" role="alert">{fieldErrors.bodyLimit}</span> : null}
            </div>
            <div className="field">
              <label htmlFor="global-max-inflight-body">Concurrent request buffer budget (MiB)</label>
              <input id="global-max-inflight-body" type="number" min={1 / MiB} step="any" value={bufferLimit} onChange={(event) => change('bufferLimit', event.target.value)} onBlur={() => validateField('bufferLimit')} aria-describedby={`global-max-inflight-body-help${fieldErrors.bufferLimit ? ' global-max-inflight-body-error' : ''}`} aria-invalid={fieldErrors.bufferLimit ? true : undefined} />
              <small id="global-max-inflight-body-help">Estimated buffering budget per process, including JSON conversion. Must be at least four times the request body limit. Choose values that leave room for other application memory.</small>
              {fieldErrors.bufferLimit ? <span className="settings-field-error" id="global-max-inflight-body-error" role="alert">{fieldErrors.bufferLimit}</span> : null}
            </div>
          </fieldset>
          {error ? <div className="inline-alert" role="alert">{error}</div> : null}
          {saved ? <p className="save-confirmation" role="status">Global settings saved.</p> : null}
          <div className="form-actions"><button className="button button--primary" type="submit" disabled={saving}>{saving ? <><Spinner /> Saving…</> : 'Save setting'}</button></div>
        </form>
      )}
    </section>
  )
}
