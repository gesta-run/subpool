import { useState } from 'react'
import { errorMessage, request } from '../../api'
import { Modal } from '../Modal'
import { SelectMenu } from '../SelectMenu'
import { Spinner } from '../Spinner'
import type { ProviderAccount } from '../../types'

export function BailianQuotaDialog({ account, onClose, onSaved }: { account: ProviderAccount; onClose: () => void; onSaved: () => Promise<void> }) {
  const [method, setMethod] = useState('browser')
  const [accessToken, setAccessToken] = useState('')
  const [accessKeyID, setAccessKeyID] = useState('')
  const [accessKeySecret, setAccessKeySecret] = useState('')
  const [action, setAction] = useState<'save' | 'remove' | null>(null)
  const [error, setError] = useState('')
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const busy = action !== null

  function validateField(name: string, value: string, message: string) {
    setFieldErrors((errors) => ({ ...errors, [name]: value.trim() ? '' : message }))
  }

  async function save(remove = false) {
    if (busy) return
    if (!remove) {
      const errors: Record<string, string> = method === 'browser'
        ? { accessToken: accessToken.trim() ? '' : 'Enter a console access token.' }
        : { accessKeyID: accessKeyID.trim() ? '' : 'Enter an AccessKey ID.', accessKeySecret: accessKeySecret.trim() ? '' : 'Enter an AccessKey secret.' }
      setFieldErrors(errors)
      const firstError = Object.entries(errors).find(([, message]) => message)
      if (firstError) {
        document.getElementsByName(firstError[0])[0]?.focus()
        return
      }
    }
    setAction(remove ? 'remove' : 'save')
    setError('')
    try {
      await request(`/api/v1/provider-accounts/${account.id}/bailian-quota`, {
        method: remove ? 'DELETE' : 'PUT',
        body: remove ? undefined : JSON.stringify(method === 'browser' ? { access_token: accessToken.trim() } : { access_key_id: accessKeyID.trim(), access_key_secret: accessKeySecret.trim() }),
      })
      await onSaved()
      onClose()
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setAction(null)
    }
  }

  return <Modal title={account.quota_authorized ? 'Manage quota authorization' : 'Connect Bailian quota'} description={`Read Token Plan usage for ${account.display_name}. Model access stays unchanged.`} onClose={onClose}>
    <form className="form-stack" onSubmit={(event) => { event.preventDefault(); void save() }} noValidate aria-busy={busy}>
      <div className="field"><label htmlFor="bailian-auth-method">Authorization method</label><SelectMenu id="bailian-auth-method" value={method} options={[{ value: 'browser', label: 'Browser token (temporary)' }, { value: 'ram', label: 'RAM AccessKey (automatic renewal)' }]} disabled={busy} onChange={(value) => { setMethod(value); setFieldErrors({}); setError('') }} />
        <small>{method === 'browser' ? "Sign in to the subscription owner's account using the official Bailian CLI browser flow. Import the console token below, or let your local agent import it." : 'Use a RAM user belonging to the same Token Plan subscription. Grant Token Plan read access in RAM and subscription access in Bailian account permissions.'}</small>
      </div>
      {method === 'browser' ? <>
        <div className="field"><label htmlFor="bailian-access-token">Console access token <span aria-hidden="true">*</span></label><input id="bailian-access-token" name="accessToken" type="password" required disabled={busy} value={accessToken} onChange={(event) => setAccessToken(event.target.value)} onBlur={() => validateField('accessToken', accessToken, 'Enter a console access token.')} aria-invalid={Boolean(fieldErrors.accessToken)} aria-describedby={fieldErrors.accessToken ? 'bailian-token-help bailian-token-error' : 'bailian-token-help'} autoComplete="off" /><small id="bailian-token-help">Temporary authorization. Please sign in again when it expires.</small>{fieldErrors.accessToken ? <small id="bailian-token-error" className="field__error">{fieldErrors.accessToken}</small> : null}</div>
      </> : <>
        <div className="field"><label htmlFor="bailian-access-key-id">AccessKey ID <span aria-hidden="true">*</span></label><input id="bailian-access-key-id" name="accessKeyID" type="password" required disabled={busy} value={accessKeyID} onChange={(event) => setAccessKeyID(event.target.value)} onBlur={() => validateField('accessKeyID', accessKeyID, 'Enter an AccessKey ID.')} aria-invalid={Boolean(fieldErrors.accessKeyID)} aria-describedby={fieldErrors.accessKeyID ? 'bailian-key-id-error' : undefined} autoComplete="off" />{fieldErrors.accessKeyID ? <small id="bailian-key-id-error" className="field__error">{fieldErrors.accessKeyID}</small> : null}</div>
        <div className="field"><label htmlFor="bailian-access-key-secret">AccessKey secret <span aria-hidden="true">*</span></label><input id="bailian-access-key-secret" name="accessKeySecret" type="password" required disabled={busy} value={accessKeySecret} onChange={(event) => setAccessKeySecret(event.target.value)} onBlur={() => validateField('accessKeySecret', accessKeySecret, 'Enter an AccessKey secret.')} aria-invalid={Boolean(fieldErrors.accessKeySecret)} aria-describedby={fieldErrors.accessKeySecret ? 'bailian-key-secret-help bailian-key-secret-error' : 'bailian-key-secret-help'} autoComplete="off" /><small id="bailian-key-secret-help">Authorization renews automatically.</small>{fieldErrors.accessKeySecret ? <small id="bailian-key-secret-error" className="field__error">{fieldErrors.accessKeySecret}</small> : null}</div>
      </>}
      <div className="field"><small>Credentials are encrypted and never returned by the API.</small></div>
      {error ? <div className="inline-alert" role="alert">{error}</div> : null}
      <div className="form-actions">
        {account.quota_authorized ? <button className="button button--secondary" type="button" disabled={busy} onClick={() => void save(true)}>{action === 'remove' ? <><Spinner /> Disconnecting…</> : 'Disconnect quota'}</button> : null}
        <button className="button button--secondary" type="button" onClick={onClose}>Cancel</button>
        <button className="button button--primary" type="submit" disabled={busy}>{action === 'save' ? <><Spinner /> {account.quota_authorized ? 'Saving…' : 'Connecting…'}</> : account.quota_authorized ? 'Save changes' : 'Connect quota'}</button>
      </div>
    </form>
  </Modal>
}
