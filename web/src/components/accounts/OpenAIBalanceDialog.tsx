import { useState } from 'react'
import { errorMessage, request } from '../../api'
import { Modal } from '../Modal'
import { Spinner } from '../Spinner'
import type { ProviderAccount } from '../../types'

export function OpenAIBalanceDialog({ account, onClose, onSaved }: { account: ProviderAccount; onClose: () => void; onSaved: () => void }) {
  const [sessionKey, setSessionKey] = useState('')
  const [organizationID, setOrganizationID] = useState('')
  const [replacing, setReplacing] = useState(!account.quota_authorized)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function save(remove = false) {
    if (busy) return
    setBusy(true)
    setError('')
    const controller = new AbortController()
    const timeout = window.setTimeout(() => controller.abort(), 20_000)
    try {
      await request(`/api/v1/provider-accounts/${account.id}/openai-balance`, {
        method: remove ? 'DELETE' : 'PUT', signal: controller.signal,
        body: remove ? undefined : JSON.stringify({ session_key: sessionKey.trim(), organization_id: organizationID.trim() }),
      })
      setSessionKey('')
      onSaved()
      onClose()
    } catch (caught) {
      setError(controller.signal.aborted ? 'The request timed out. Refresh the account before retrying; the authorization may already have been saved.' : errorMessage(caught))
    } finally {
      window.clearTimeout(timeout)
      setBusy(false)
    }
  }

  return <Modal title={account.quota_authorized ? 'Manage OpenAI billing' : 'Connect OpenAI billing'} description={`Read API credit balance for ${account.display_name}. Model access stays unchanged.`} onClose={() => { if (!busy) onClose() }} showClose={!busy}>
    <form className="form-stack" onSubmit={(event) => { event.preventDefault(); void save() }} aria-busy={busy}>
      {replacing ? <>
        <div className="field"><label htmlFor="openai-billing-org">Organization ID</label><input id="openai-billing-org" required placeholder="org-…" value={organizationID} onChange={(event) => setOrganizationID(event.target.value)} disabled={busy} autoComplete="off" /></div>
        <div className="field"><label htmlFor="openai-billing-session">Browser session key</label><input id="openai-billing-session" required type="password" value={sessionKey} onChange={(event) => setSessionKey(event.target.value)} disabled={busy} autoComplete="off" /><small>Use the matching API Platform browser authorization. A model API key cannot read the balance. Your local agent can import this authorization.</small></div>
      </> : <div className="field"><strong>Billing authorization saved securely</strong><small>Reconnect when this temporary browser authorization expires.</small></div>}
      <div className="field"><small>Browser credentials may allow other dashboard actions. SubPool encrypts them and uses them only to read your balance. They are never returned by the API.</small></div>
      {error ? <div className="inline-alert" role="alert">{error}</div> : null}
      <div className="form-actions">
        <button className="button button--secondary" type="button" disabled={busy} onClick={onClose}>Close</button>
        {account.quota_authorized ? <button className="button button--secondary" type="button" disabled={busy} onClick={() => void save(true)}>Disconnect billing</button> : null}
        {replacing ? <button className="button button--primary" type="submit" disabled={busy}>{busy ? <Spinner /> : null}Save authorization</button> : <button className="button button--primary" type="button" disabled={busy} onClick={() => setReplacing(true)}>Replace authorization</button>}
      </div>
    </form>
  </Modal>
}
