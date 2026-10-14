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
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function save(remove = false) {
    setBusy(true)
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
      setBusy(false)
    }
  }

  return <Modal title="Connect Bailian quota" description={`Read Token Plan usage for ${account.display_name}. Model access stays unchanged.`} onClose={onClose}>
    <form className="form-grid" onSubmit={(event) => { event.preventDefault(); void save() }}>
      <div className="field"><label htmlFor="bailian-auth-method">Authorization method</label><SelectMenu id="bailian-auth-method" value={method} options={[{ value: 'browser', label: 'Browser token (temporary)' }, { value: 'ram', label: 'RAM AccessKey (automatic renewal)' }]} onChange={setMethod} /></div>
      {method === 'browser' ? <>
        <p>Sign in to the subscription owner's account using the official Bailian CLI browser flow. Import the console token below, or let your local agent import it. This is a temporary authorization; sign in again when it expires.</p>
        <div className="field"><label htmlFor="bailian-access-token">Console access token</label><input id="bailian-access-token" type="password" required value={accessToken} onChange={(event) => setAccessToken(event.target.value)} autoComplete="off" /></div>
      </> : <>
        <p>Use a RAM user belonging to the same Token Plan subscription. Grant Token Plan read access in RAM and subscription access in Bailian account permissions. Authorization renews automatically.</p>
        <div className="field"><label htmlFor="bailian-access-key-id">AccessKey ID</label><input id="bailian-access-key-id" type="password" required value={accessKeyID} onChange={(event) => setAccessKeyID(event.target.value)} autoComplete="off" /></div>
        <div className="field"><label htmlFor="bailian-access-key-secret">AccessKey secret</label><input id="bailian-access-key-secret" type="password" required value={accessKeySecret} onChange={(event) => setAccessKeySecret(event.target.value)} autoComplete="off" /></div>
      </>}
      <small>Credentials are encrypted and never returned by the API. Model access stays unchanged.</small>
      {error ? <div className="inline-alert" role="alert">{error}</div> : null}
      <div className="form-actions">
        {account.quota_authorized ? <button className="button button--secondary" type="button" disabled={busy} onClick={() => void save(true)}>Disconnect quota</button> : null}
        <button className="button button--secondary" type="button" onClick={onClose}>Cancel</button>
        <button className="button button--primary" type="submit" disabled={busy || (method === 'browser' ? !accessToken.trim() : !accessKeyID.trim() || !accessKeySecret.trim())}>{busy ? <><Spinner /> Connecting…</> : 'Connect quota'}</button>
      </div>
    </form>
  </Modal>
}
