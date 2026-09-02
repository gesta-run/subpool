import { useState } from 'react'
import { errorMessage, request } from '../api'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { CopyIcon, KeyIcon, PlusIcon } from '../components/Icons'
import { Modal } from '../components/Modal'
import { PageSkeleton } from '../components/PageSkeleton'
import { SelectMenu } from '../components/SelectMenu'
import { StatePanel } from '../components/StatePanel'
import { Spinner } from '../components/Spinner'
import { useRemoteList } from '../hooks/useRemoteList'
import type { APIKeyRecord, Pool, ProviderAccount } from '../types'

interface APIKeysBodyProps {
  accounts: ProviderAccount[]
  actionError: string
  activeKeys: APIKeyRecord[]
  loadError: string
  loading: boolean
  pools: Pool[]
  usedKeys: number
  onCreate: () => void
  onReload: () => void
  onRemove: (key: APIKeyRecord) => void
}

function APIKeysBody(props: APIKeysBodyProps) {
  if (props.loading) return <PageSkeleton metrics={2} variant="table" />
  return <>
    <div className="metric-strip metric-strip--two" aria-label="API key summary">
      <article><span>Active keys</span><strong>{props.activeKeys.length}</strong></article>
      <article><span>Used at least once</span><strong>{props.usedKeys}</strong></article>
    </div>
    {props.actionError ? <div className="inline-alert" role="alert">{props.actionError}</div> : null}
    {props.loadError ? <StatePanel kind="error" title="API keys unavailable" description={props.loadError} actionLabel="Try again" onAction={props.onReload} /> : props.activeKeys.length === 0 ? <StatePanel kind="empty" title="No API keys issued" description="Create an employee key after at least one account pool is ready." actionLabel="Create API key" onAction={props.onCreate} /> : <div className="table-frame"><table><thead><tr><th>Employee</th><th>Key</th><th>Pool</th><th>Bound account</th><th>Last used</th><th>Created</th><th>Status</th><th><span className="sr-only">Actions</span></th></tr></thead>
      <tbody>{props.activeKeys.map((key) => <tr key={key.id}>
        <td data-label="Employee"><strong>{key.employee_name}</strong></td><td data-label="Key"><code>sk-••••{key.key_hint}</code></td>
        <td data-label="Pool">{key.pool_name ?? props.pools.find((pool) => pool.id === key.pool_id)?.name ?? key.pool_id}</td>
        <td data-label="Bound account">{props.accounts.find((account) => account.id === key.provider_account_id)?.display_name ?? key.provider_account_id ?? 'Not assigned'}</td>
        <td data-label="Last used">{key.last_used_at ? new Date(key.last_used_at).toLocaleString() : 'Never'}</td><td data-label="Created">{new Date(key.created_at).toLocaleDateString()}</td>
        <td data-label="Status"><span className="status status--active"><i />active</span></td><td><button className="text-button text-button--danger" type="button" onClick={() => props.onRemove(key)}>Remove</button></td>
      </tr>)}</tbody></table></div>}
  </>
}

function CreateKeyDialog(props: { actionError: string; employee: string; expiryDays: string; poolID: string; pools: Pool[]; poolsLoading: boolean; saving: boolean; onClose: () => void; onCreate: () => void; onEmployeeChange: (value: string) => void; onExpiryChange: (value: string) => void; onPoolChange: (value: string) => void }) {
  return <Modal title="Create employee API key" description="Subpool automatically assigns this key to an account with an available slot." onClose={props.onClose}>
    <div className="form-stack">
      <div className="field"><label htmlFor="employee-name">Employee name <span aria-hidden="true">*</span></label><input id="employee-name" value={props.employee} onChange={(event) => props.onEmployeeChange(event.target.value)} placeholder="Alex Chen" /></div>
      <div className="field"><label htmlFor="key-pool">Account pool <span aria-hidden="true">*</span></label><SelectMenu id="key-pool" value={props.poolID || props.pools[0]?.id || ''} onChange={props.onPoolChange} disabled={props.poolsLoading || props.pools.length === 0} options={props.pools.length > 0 ? props.pools.map((pool) => ({ value: pool.id, label: pool.name })) : [{ value: '', label: 'No pools available', disabled: true }]} /></div>
      <div className="field"><label htmlFor="key-expiry">Expiration</label><SelectMenu id="key-expiry" value={props.expiryDays} onChange={props.onExpiryChange} options={[{ value: 'never', label: 'Never' }, { value: '30', label: '30 days' }, { value: '60', label: '60 days' }, { value: '90', label: '90 days' }]} /></div>
      {props.actionError ? <div className="inline-alert" role="alert">{props.actionError}</div> : null}
      <div className="form-actions"><button className="button button--secondary" type="button" onClick={props.onClose}>Cancel</button><button className="button button--primary" type="button" disabled={props.saving || props.poolsLoading} onClick={props.onCreate}>{props.saving ? <><Spinner /> Creating…</> : 'Create key'}</button></div>
    </div>
  </Modal>
}

function CreatedKeyDialog({ copied, secret, onClose, onCopy }: { copied: boolean; secret: string; onClose: () => void; onCopy: () => void }) {
  return <Modal title="Copy this key now" description="For security, Subpool will not show the complete key again." onClose={onClose}>
    <div className="secret-box"><KeyIcon /><code data-testid="created-key">{secret}</code></div>
    <div className="inline-alert inline-alert--warning" role="status"><strong>Store it securely.</strong><span>Closing this window permanently hides the complete value.</span></div>
    <div className="form-actions"><button className="button button--secondary" type="button" onClick={onClose}>I have saved it</button><button className="button button--primary" type="button" onClick={onCopy}>{copied ? 'Copied' : <><CopyIcon className="button__icon" /> Copy API key</>}</button></div>
  </Modal>
}

export function APIKeysPage() {
  const keys = useRemoteList<APIKeyRecord>('/api/v1/api-keys', ['api_keys', 'keys'])
  const pools = useRemoteList<Pool>('/api/v1/pools', ['pools'])
  const accounts = useRemoteList<ProviderAccount>('/api/v1/provider-accounts', ['provider_accounts', 'accounts'])
  const [showCreate, setShowCreate] = useState(false)
  const [employee, setEmployee] = useState('')
  const [poolID, setPoolID] = useState('')
  const [expiryDays, setExpiryDays] = useState('never')
  const [createdKey, setCreatedKey] = useState('')
  const [copied, setCopied] = useState(false)
  const [saving, setSaving] = useState(false)
  const [actionError, setActionError] = useState('')
  const [pendingRemove, setPendingRemove] = useState<APIKeyRecord | null>(null)

  const activeKeys = keys.items.filter((key) => !key.revoked_at)
  const usedKeys = activeKeys.filter((key) => key.last_used_at).length
  const loading = keys.loading || pools.loading || accounts.loading
  const loadError = keys.error || pools.error || accounts.error

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
      const expiresAt = expiryDays === 'never' ? null : new Date(Date.now() + Number(expiryDays) * 24 * 60 * 60 * 1000).toISOString()
      const result = await request<Record<string, unknown>>('/api/v1/api-keys', {
        method: 'POST',
        body: JSON.stringify({ employee_name: employee.trim(), pool_id: selectedPool, expires_at: expiresAt }),
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
    setExpiryDays('never')
    setCopied(false)
  }

  return (
    <section aria-labelledby="keys-heading">
      <header className="page-heading">
        <div><h2 id="keys-heading">API keys</h2><p>Issue one opaque key per employee. The full value is shown exactly once.</p></div>
        {!loading && !loadError && activeKeys.length > 0 ? <button className="button button--primary" type="button" onClick={() => { setActionError(''); setShowCreate(true) }}><PlusIcon className="button__icon" /> Create API key</button> : null}
      </header>
      <APIKeysBody accounts={accounts.items} actionError={!showCreate ? actionError : ''} activeKeys={activeKeys} loadError={loadError} loading={loading} pools={pools.items} usedKeys={usedKeys} onCreate={() => setShowCreate(true)} onReload={() => { void keys.reload(); void pools.reload(); void accounts.reload() }} onRemove={setPendingRemove} />
      {showCreate ? <CreateKeyDialog actionError={actionError} employee={employee} expiryDays={expiryDays} poolID={poolID} pools={pools.items} poolsLoading={pools.loading} saving={saving} onClose={() => setShowCreate(false)} onCreate={() => void createKey()} onEmployeeChange={setEmployee} onExpiryChange={setExpiryDays} onPoolChange={setPoolID} /> : null}
      {createdKey ? <CreatedKeyDialog copied={copied} secret={createdKey} onClose={closeSecret} onCopy={() => void copyCreatedKey()} /> : null}
      {pendingRemove ? <ConfirmDialog
        title={`Remove ${pendingRemove.employee_name}'s API key?`}
        description="This key will stop working immediately. Historical token usage will be retained."
        confirmLabel="Remove API key"
        onCancel={() => setPendingRemove(null)}
        onConfirm={() => { const key = pendingRemove; setPendingRemove(null); void revokeKey(key) }}
      /> : null}
    </section>
  )
}
