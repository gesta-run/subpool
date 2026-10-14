import { useState } from 'react'
import { errorMessage, request } from '../../api'
import { Modal } from '../Modal'
import { Spinner } from '../Spinner'
import type { ProviderAccount } from '../../types'

export function BailianQuotaDialog({ account, onClose, onSaved }: { account: ProviderAccount; onClose: () => void; onSaved: () => Promise<void> }) {
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
        body: remove ? undefined : JSON.stringify({ access_key_id: accessKeyID.trim(), access_key_secret: accessKeySecret.trim() }),
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
      <p>Use a RAM user belonging to the same Token Plan subscription. Grant Token Plan read access in RAM and subscription access in Bailian account permissions. Subpool refreshes authorization automatically and encrypts these credentials.</p>
      <div className="field"><label htmlFor="bailian-access-key-id">AccessKey ID</label><input id="bailian-access-key-id" type="password" required value={accessKeyID} onChange={(event) => setAccessKeyID(event.target.value)} autoComplete="off" /></div>
      <div className="field"><label htmlFor="bailian-access-key-secret">AccessKey secret</label><input id="bailian-access-key-secret" type="password" required value={accessKeySecret} onChange={(event) => setAccessKeySecret(event.target.value)} autoComplete="off" /></div>
      {error ? <div className="inline-alert" role="alert">{error}</div> : null}
      <div className="form-actions">
        {account.quota_authorized ? <button className="button button--secondary" type="button" disabled={busy} onClick={() => void save(true)}>Disconnect quota</button> : null}
        <button className="button button--secondary" type="button" onClick={onClose}>Cancel</button>
        <button className="button button--primary" type="submit" disabled={busy || !accessKeyID.trim() || !accessKeySecret.trim()}>{busy ? <><Spinner /> Connecting…</> : 'Connect quota'}</button>
      </div>
    </form>
  </Modal>
}
