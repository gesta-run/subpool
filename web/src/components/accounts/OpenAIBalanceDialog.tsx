import { useState } from 'react'
import { errorMessage, request } from '../../api'
import { Modal } from '../Modal'
import { Spinner } from '../Spinner'
import type { ProviderAccount } from '../../types'

function grantDate(seconds?: number) {
  return seconds ? new Date(seconds * 1000 + 8 * 3600_000).toISOString().slice(0, 19) : ''
}

export function OpenAIBalanceDialog({ account, onClose, onSaved }: { account: ProviderAccount; onClose: () => void; onSaved: () => void }) {
  const [method, setMethod] = useState(account.quota_authorization_method === 'browser' ? 'browser' : 'costs')
  const [secret, setSecret] = useState('')
  const [organizationID, setOrganizationID] = useState('')
  const balance = account.quota_snapshot?.balance
  const [grantAmount, setGrantAmount] = useState(balance?.granted_usd?.toString() ?? '')
  const [grantStart, setGrantStart] = useState(grantDate(balance?.grant_start_at))
  const [grantExpiry, setGrantExpiry] = useState(grantDate(balance?.expires_at))
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
      const body = method === 'costs' ? {
        organization_id: organizationID.trim(), admin_key: secret.trim(), grant_amount_usd: Number(grantAmount),
        grant_start_at: Date.parse(`${grantStart}+08:00`) / 1000, grant_expires_at: Date.parse(`${grantExpiry}+08:00`) / 1000,
      } : { session_key: secret.trim(), organization_id: organizationID.trim() }
      await request(`/api/v1/provider-accounts/${account.id}/openai-balance`, {
        method: remove ? 'DELETE' : 'PUT', signal: controller.signal,
        body: remove ? undefined : JSON.stringify(body),
      })
      setSecret('')
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
        <div className="field"><label htmlFor="openai-billing-method">Balance source</label><select id="openai-billing-method" value={method} disabled={busy} onChange={(event) => { setMethod(event.target.value); setSecret('') }}><option value="costs">Estimated balance · Costs API</option><option value="browser">Exact balance · browser authorization</option></select></div>
        <div className="field"><label htmlFor="openai-billing-org">Organization ID</label><input id="openai-billing-org" required placeholder="org-…" value={organizationID} onChange={(event) => setOrganizationID(event.target.value)} disabled={busy} autoComplete="off" /></div>
        <div className="field"><label htmlFor="openai-billing-secret">{method === 'costs' ? 'Admin API key' : 'Browser session key'}</label><input id="openai-billing-secret" required type="password" value={secret} onChange={(event) => setSecret(event.target.value)} disabled={busy} autoComplete="off" /></div>
        {method === 'costs' ? <>
          <div className="field"><label htmlFor="openai-grant-amount">Grant amount (USD)</label><input id="openai-grant-amount" required type="number" min="0.01" step="0.01" value={grantAmount} onChange={(event) => setGrantAmount(event.target.value)} disabled={busy} /></div>
          <div className="field"><label htmlFor="openai-grant-start">Grant start (Beijing time)</label><input id="openai-grant-start" required type="datetime-local" step="1" value={grantStart} onChange={(event) => setGrantStart(event.target.value)} disabled={busy} /></div>
          <div className="field"><label htmlFor="openai-grant-expiry">Grant expiry (Beijing time)</label><input id="openai-grant-expiry" required type="datetime-local" step="1" value={grantExpiry} onChange={(event) => setGrantExpiry(event.target.value)} disabled={busy} /></div>
          <small>Estimated grant minus organization-wide Costs API spend. Costs can lag. Update this grant when new credits arrive; it is not a payment limit.</small>
        </> : <small>Temporary API Platform browser authorization. Reconnect when it expires. A model API key cannot read the balance.</small>}
      </> : <div className="field"><strong>Billing authorization saved securely</strong><small>{account.quota_authorization_method === 'costs' ? 'Costs API estimate. Replace authorization to update the grant or Admin key.' : 'Reconnect when this temporary browser authorization expires.'}</small></div>}
      <div className="field"><small>Billing credentials are encrypted and never returned by the API. SubPool uses them only for balance and cost queries.</small></div>
      {error ? <div className="inline-alert" role="alert">{error}</div> : null}
      <div className="form-actions">
        <button className="button button--secondary" type="button" disabled={busy} onClick={onClose}>Close</button>
        {account.quota_authorized ? <button className="button button--secondary" type="button" disabled={busy} onClick={() => void save(true)}>Disconnect billing</button> : null}
        {replacing ? <button className="button button--primary" type="submit" disabled={busy}>{busy ? <Spinner /> : null}Save authorization</button> : <button className="button button--primary" type="button" disabled={busy} onClick={() => setReplacing(true)}>Replace authorization</button>}
      </div>
    </form>
  </Modal>
}
