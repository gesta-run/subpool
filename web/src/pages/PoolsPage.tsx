import { useState } from 'react'
import { errorMessage, request } from '../api'
import { ChevronIcon, PlusIcon, PoolIcon } from '../components/Icons'
import { Modal } from '../components/Modal'
import { StatePanel } from '../components/StatePanel'
import { useRemoteList } from '../hooks/useRemoteList'
import type { Pool, ProviderAccount } from '../types'

export function PoolsPage() {
  const { items, loading, error, reload } = useRemoteList<Pool>('/api/v1/pools', ['pools'])
  const accounts = useRemoteList<ProviderAccount>('/api/v1/provider-accounts', ['provider_accounts', 'accounts'])
  const [showCreate, setShowCreate] = useState(false)
  const [managePool, setManagePool] = useState<Pool | null>(null)
  const [accountID, setAccountID] = useState('')
  const [manageName, setManageName] = useState('')
  const [manageModels, setManageModels] = useState('')
  const [name, setName] = useState('')
  const [models, setModels] = useState('gpt-5-codex')
  const [saving, setSaving] = useState(false)
  const [actionError, setActionError] = useState('')

  async function createPool() {
    const modelAllowlist = models.split(',').map((model) => model.trim()).filter(Boolean)
    if (!name.trim()) {
      setActionError('Enter a pool name.')
      return
    }
    if (modelAllowlist.length === 0) {
      setActionError('Enter at least one allowed model ID.')
      return
    }
    setSaving(true)
    setActionError('')
    try {
      await request('/api/v1/pools', {
        method: 'POST',
        body: JSON.stringify({
          name: name.trim(),
          model_allowlist: modelAllowlist,
        }),
      })
      setShowCreate(false)
      setName('')
      await reload()
    } catch (caught) {
      setActionError(errorMessage(caught))
    } finally {
      setSaving(false)
    }
  }

  async function attachAccount() {
    if (!managePool || !accountID) {
      setActionError('Select an account to attach.')
      return
    }
    setSaving(true)
    setActionError('')
    try {
      await request(`/api/v1/pools/${managePool.id}/accounts`, {
        method: 'POST',
        body: JSON.stringify({ provider_account_id: accountID, weight: 1, priority: 0, enabled: true }),
      })
      setManagePool(null)
      setAccountID('')
      await reload()
    } catch (caught) {
      setActionError(errorMessage(caught))
    } finally {
      setSaving(false)
    }
  }

  async function savePoolSettings() {
    if (!managePool) return
    const modelAllowlist = manageModels.split(',').map((model) => model.trim()).filter(Boolean)
    if (!manageName.trim()) {
      setActionError('Enter a pool name.')
      return
    }
    if (modelAllowlist.length === 0) {
      setActionError('Enter at least one allowed model ID.')
      return
    }
    setSaving(true)
    setActionError('')
    try {
      await request(`/api/v1/pools/${managePool.id}`, {
        method: 'PUT',
        body: JSON.stringify({ name: manageName.trim(), model_allowlist: modelAllowlist }),
      })
      setManagePool(null)
      await reload()
    } catch (caught) {
      setActionError(errorMessage(caught))
    } finally {
      setSaving(false)
    }
  }

  const accountTotal = items.reduce((sum, pool) => sum + (pool.account_count ?? pool.accounts?.length ?? 0), 0)

  return (
    <section aria-labelledby="pools-heading">
      <header className="page-heading">
        <div>
          <p className="eyebrow">Routing groups</p>
          <h2 id="pools-heading">Account pools</h2>
          <p>Group compatible Codex accounts behind a stable employee-facing endpoint.</p>
        </div>
        <button className="button button--primary" type="button" onClick={() => setShowCreate(true)}><PlusIcon className="button__icon" /> Create pool</button>
      </header>
      <div className="metric-strip metric-strip--two" aria-label="Pool summary">
        <article><span>Active pools</span><strong>{items.filter((pool) => pool.enabled !== false).length}</strong></article>
        <article><span>Account memberships</span><strong>{items.some((pool) => pool.account_count != null || pool.accounts != null) ? accountTotal : '—'}</strong></article>
      </div>
      {actionError && !showCreate && !managePool ? <div className="inline-alert" role="alert">{actionError}</div> : null}
      {loading ? <StatePanel kind="loading" title="Loading pools" description="Reading routing groups and model policy." /> :
        error ? <StatePanel kind="error" title="Pools unavailable" description={error} actionLabel="Try again" onAction={() => void reload()} /> :
        items.length === 0 ? <StatePanel kind="empty" title="No pools configured" description="Create a pool, then attach at least one healthy Codex account." actionLabel="Create pool" onAction={() => setShowCreate(true)} /> : (
          <div className="pool-list">{items.map((pool, index) => {
            const accountCount = pool.account_count ?? pool.accounts?.length ?? 0
            return (
              <article className="pool-row" key={pool.id}>
                <span className="pool-row__index">{String(index + 1).padStart(2, '0')}</span>
                <span className="pool-row__icon"><PoolIcon /></span>
                <div className="pool-row__main"><div><h3>{pool.name}</h3><span className={`status ${pool.enabled === false ? 'status--disabled' : 'status--active'}`}><i />{pool.enabled === false ? 'disabled' : 'active'}</span></div><p>{pool.provider} · {pool.strategy.replaceAll('_', ' ')}</p></div>
                <div className="pool-row__meta"><span>ACCOUNTS<strong>{accountCount}</strong></span><span>MODELS<strong>{pool.model_allowlist?.length ? pool.model_allowlist.join(', ') : 'All Codex'}</strong></span></div>
                <button className="icon-button" type="button" aria-label={`Manage ${pool.name}`} onClick={() => { setActionError(''); setManagePool(pool); setManageName(pool.name); setManageModels(pool.model_allowlist?.join(', ') ?? '') }}><ChevronIcon /></button>
              </article>
            )
          })}</div>
        )}
      {showCreate ? (
        <Modal title="Create account pool" description="The first release uses least-assigned routing within a Codex-only pool." onClose={() => setShowCreate(false)}>
          <div className="form-stack">
            <div className="field"><label htmlFor="pool-name">Pool name <span aria-hidden="true">*</span></label><input id="pool-name" value={name} onChange={(event) => setName(event.target.value)} placeholder="Engineering" /></div>
            <div className="field"><label htmlFor="pool-provider">Provider</label><input id="pool-provider" value="Codex subscription" disabled /><small>Mixed-provider pools are intentionally not supported.</small></div>
            <div className="field"><label htmlFor="pool-models">Allowed models <span aria-hidden="true">*</span></label><input id="pool-models" value={models} onChange={(event) => setModels(event.target.value)} placeholder="gpt-5-codex" /><small>At least one model ID is required. Separate multiple IDs with commas.</small></div>
            <div className="field"><label htmlFor="pool-strategy">Assignment strategy</label><input id="pool-strategy" value="Least assigned" disabled /><small>New keys are assigned to the healthy account with the most available slots.</small></div>
            {actionError ? <div className="inline-alert" role="alert">{actionError}</div> : null}
            <div className="form-actions"><button className="button button--secondary" type="button" onClick={() => setShowCreate(false)}>Cancel</button><button className="button button--primary" type="button" disabled={saving} onClick={() => void createPool()}>{saving ? 'Creating…' : 'Create pool'}</button></div>
          </div>
        </Modal>
      ) : null}
      {managePool ? (
        <Modal title={`Manage ${managePool.name}`} description="Attach a healthy Codex account to this routing pool." onClose={() => setManagePool(null)}>
          <div className="form-stack">
            <div className="field"><label htmlFor="manage-pool-name">Pool name <span aria-hidden="true">*</span></label><input id="manage-pool-name" value={manageName} onChange={(event) => setManageName(event.target.value)} /></div>
            <div className="field"><label htmlFor="manage-pool-models">Allowed models <span aria-hidden="true">*</span></label><input id="manage-pool-models" value={manageModels} onChange={(event) => setManageModels(event.target.value)} placeholder="gpt-5-codex" /><small>At least one model ID is required. Separate multiple IDs with commas.</small></div>
            <div className="form-actions"><button className="button button--secondary" type="button" onClick={() => setManagePool(null)}>Cancel</button><button className="button button--primary" type="button" disabled={saving} onClick={() => void savePoolSettings()}>{saving ? 'Saving…' : 'Save settings'}</button></div>
            <div className="member-list" aria-label="Current pool members">
              <span className="member-list__label">Current members</span>
              {managePool.accounts?.length ? managePool.accounts.map((membership) => {
                const account = accounts.items.find((item) => item.id === membership.provider_account_id)
                return <div className="member-list__row" key={membership.provider_account_id}><span><strong>{account?.display_name ?? membership.provider_account_id}</strong><small>{membership.enabled === false ? 'Membership disabled' : account?.status ?? 'Enabled'}</small></span><span className={`status ${membership.enabled === false || account?.status === 'disabled' ? 'status--disabled' : 'status--active'}`}><i />{membership.enabled === false ? 'disabled' : 'enabled'}</span></div>
              }) : <p>No accounts attached yet.</p>}
            </div>
            <div className="field"><label htmlFor="pool-account">Codex account <span aria-hidden="true">*</span></label><select id="pool-account" value={accountID} onChange={(event) => setAccountID(event.target.value)} disabled={accounts.loading || accounts.items.length === 0}><option value="">Select an account</option>{accounts.items.filter((account) => account.status === 'active' && !managePool.accounts?.some((membership) => membership.provider_account_id === account.id)).map((account) => <option key={account.id} value={account.id}>{account.display_name} · {account.status}</option>)}</select>{accounts.items.length === 0 && !accounts.loading ? <small>Connect an account from the Accounts page first.</small> : null}</div>
            {actionError ? <div className="inline-alert" role="alert">{actionError}</div> : null}
            <div className="form-actions"><button className="button button--secondary" type="button" onClick={() => setManagePool(null)}>Cancel</button><button className="button button--primary" type="button" disabled={saving || accounts.loading || accounts.items.length === 0} onClick={() => void attachAccount()}>{saving ? 'Attaching…' : 'Attach account'}</button></div>
          </div>
        </Modal>
      ) : null}
    </section>
  )
}
