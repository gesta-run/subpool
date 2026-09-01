import { useState } from 'react'
import { errorMessage, request } from '../api'
import { CopyIcon, KeyIcon, PlusIcon } from '../components/Icons'
import { Modal } from '../components/Modal'
import { StatePanel } from '../components/StatePanel'
import { useRemoteList } from '../hooks/useRemoteList'
import type { APIKeyRecord, Pool, ProviderAccount } from '../types'

export function APIKeysPage() {
  const keys = useRemoteList<APIKeyRecord>('/api/v1/api-keys', ['api_keys', 'keys'])
  const pools = useRemoteList<Pool>('/api/v1/pools', ['pools'])
  const accounts = useRemoteList<ProviderAccount>('/api/v1/provider-accounts', ['provider_accounts', 'accounts'])
  const [showCreate, setShowCreate] = useState(false)
  const [employee, setEmployee] = useState('')
  const [poolID, setPoolID] = useState('')
  const [expiresAt, setExpiresAt] = useState('')
  const [createdKey, setCreatedKey] = useState('')
  const [copied, setCopied] = useState(false)
  const [saving, setSaving] = useState(false)
  const [actionError, setActionError] = useState('')

  const activeKeys = keys.items.filter((key) => !key.revoked_at)
  const usedKeys = activeKeys.filter((key) => key.last_used_at).length

  async function createKey() {
    const selectedPool = poolID || pools.items[0]?.id
    if (!employee.trim()) {
      setActionError('Enter the employee name.')
      return
    }
    if (!selectedPool) {
      setActionError('Create a pool before issuing an API key.')
      return
    }
    setSaving(true)
    setActionError('')
    try {
      const result = await request<Record<string, unknown>>('/api/v1/api-keys', {
        method: 'POST',
        body: JSON.stringify({ employee_name: employee.trim(), pool_id: selectedPool, expires_at: expiresAt ? new Date(`${expiresAt}T23:59:59Z`).toISOString() : null }),
      })
      const secret = String(result.key ?? result.api_key ?? result.secret ?? '')
      if (!secret) throw new Error('The server created the key but did not return its one-time secret.')
      setCreatedKey(secret)
      setShowCreate(false)
      await keys.reload()
    } catch (caught) {
      setActionError(errorMessage(caught))
    } finally {
      setSaving(false)
    }
  }

  async function revokeKey(key: APIKeyRecord) {
    if (!window.confirm(`Revoke the API key assigned to ${key.employee_name}? This cannot be undone.`)) return
    setActionError('')
    try {
      await request(`/api/v1/api-keys/${key.id}/revoke`, { method: 'POST' })
      await keys.reload()
    } catch (caught) {
      setActionError(errorMessage(caught))
    }
  }

  async function copyCreatedKey() {
    await navigator.clipboard.writeText(createdKey)
    setCopied(true)
  }

  function closeSecret() {
    setCreatedKey('')
    setEmployee('')
    setExpiresAt('')
    setCopied(false)
  }

  return (
    <section aria-labelledby="keys-heading">
      <header className="page-heading">
        <div><p className="eyebrow">Employee access</p><h2 id="keys-heading">API keys</h2><p>Issue one opaque key per employee. The full value is shown exactly once.</p></div>
        <button className="button button--primary" type="button" onClick={() => { setActionError(''); setShowCreate(true) }}><PlusIcon className="button__icon" /> Create API key</button>
      </header>
      <div className="metric-strip metric-strip--two" aria-label="API key summary">
        <article><span>Active keys</span><strong>{activeKeys.length}</strong></article>
        <article><span>Used at least once</span><strong>{usedKeys}</strong></article>
      </div>
      {actionError && !showCreate ? <div className="inline-alert" role="alert">{actionError}</div> : null}
      {keys.loading ? <StatePanel kind="loading" title="Loading API keys" description="Reading employee access and recent use." /> :
        keys.error ? <StatePanel kind="error" title="API keys unavailable" description={keys.error} actionLabel="Try again" onAction={() => void keys.reload()} /> :
        keys.items.length === 0 ? <StatePanel kind="empty" title="No API keys issued" description="Create an employee key after at least one account pool is ready." actionLabel="Create API key" onAction={() => setShowCreate(true)} /> : (
          <div className="table-frame"><table><thead><tr><th>Employee</th><th>Key</th><th>Pool</th><th>Bound account</th><th>Last used</th><th>Created</th><th>Status</th><th><span className="sr-only">Actions</span></th></tr></thead>
            <tbody>{keys.items.map((key) => <tr key={key.id}>
              <td data-label="Employee"><strong>{key.employee_name}</strong></td>
              <td data-label="Key"><code>sk-subpool-••••{key.key_hint}</code></td>
              <td data-label="Pool">{key.pool_name ?? pools.items.find((pool) => pool.id === key.pool_id)?.name ?? key.pool_id}</td>
              <td data-label="Bound account">{accounts.items.find((account) => account.id === key.provider_account_id)?.display_name ?? key.provider_account_id ?? 'Not assigned'}</td>
              <td data-label="Last used">{key.last_used_at ? new Date(key.last_used_at).toLocaleString() : 'Never'}</td>
              <td data-label="Created">{new Date(key.created_at).toLocaleDateString()}</td>
              <td data-label="Status"><span className={`status ${key.revoked_at ? 'status--disabled' : 'status--active'}`}><i />{key.revoked_at ? 'revoked' : 'active'}</span></td>
              <td>{!key.revoked_at ? <button className="text-button text-button--danger" type="button" onClick={() => void revokeKey(key)}>Revoke</button> : null}</td>
            </tr>)}</tbody></table></div>
        )}
      {showCreate ? (
        <Modal title="Create employee API key" description="Subpool automatically assigns this key to an account with an available slot." onClose={() => setShowCreate(false)}>
          <div className="form-stack">
            <div className="field"><label htmlFor="employee-name">Employee name <span aria-hidden="true">*</span></label><input id="employee-name" value={employee} onChange={(event) => setEmployee(event.target.value)} placeholder="Alex Chen" /></div>
            <div className="field"><label htmlFor="key-pool">Account pool <span aria-hidden="true">*</span></label><select id="key-pool" value={poolID || pools.items[0]?.id || ''} onChange={(event) => setPoolID(event.target.value)} disabled={pools.loading || pools.items.length === 0}><option value="">Select a pool</option>{pools.items.map((pool) => <option value={pool.id} key={pool.id}>{pool.name}</option>)}</select></div>
            <div className="field"><label htmlFor="key-expiry">Expiration date</label><input id="key-expiry" type="date" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} /></div>
            {actionError ? <div className="inline-alert" role="alert">{actionError}</div> : null}
            <div className="form-actions"><button className="button button--secondary" type="button" onClick={() => setShowCreate(false)}>Cancel</button><button className="button button--primary" type="button" disabled={saving || pools.loading} onClick={() => void createKey()}>{saving ? 'Creating…' : 'Create key'}</button></div>
          </div>
        </Modal>
      ) : null}
      {createdKey ? (
        <Modal title="Copy this key now" description="For security, Subpool will not show the complete key again." onClose={closeSecret}>
          <div className="secret-box"><KeyIcon /><code data-testid="created-key">{createdKey}</code></div>
          <div className="inline-alert inline-alert--warning" role="status"><strong>Store it securely.</strong><span>Closing this window permanently hides the complete value.</span></div>
          <div className="form-actions"><button className="button button--secondary" type="button" onClick={closeSecret}>I have saved it</button><button className="button button--primary" type="button" onClick={() => void copyCreatedKey()}>{copied ? 'Copied' : <><CopyIcon className="button__icon" /> Copy API key</>}</button></div>
        </Modal>
      ) : null}
    </section>
  )
}
