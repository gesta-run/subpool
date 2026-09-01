import { useState } from 'react'
import { errorMessage, request } from '../api'
import { PlusIcon, RefreshIcon } from '../components/Icons'
import { Modal } from '../components/Modal'
import { StatePanel } from '../components/StatePanel'
import { useRemoteList } from '../hooks/useRemoteList'
import type { ProviderAccount } from '../types'

function statusLabel(status: ProviderAccount['status']) {
  return status.replaceAll('_', ' ')
}

function accountCapacity(account: ProviderAccount) {
  const assigned = account.assigned_api_keys ?? 0
  const max = account.max_api_keys || 0
  return { assigned, max, percent: max > 0 ? Math.min((assigned / max) * 100, 100) : 0 }
}

export function AccountsPage() {
  const { items, loading, error, reload } = useRemoteList<ProviderAccount>('/api/v1/provider-accounts', ['provider_accounts', 'accounts'])
  const [showImport, setShowImport] = useState(false)
  const [displayName, setDisplayName] = useState('')
  const [actionError, setActionError] = useState('')
  const [busyID, setBusyID] = useState('')

  const healthy = items.filter((item) => item.status === 'active').length
  const assigned = items.reduce((total, account) => total + (account.assigned_api_keys ?? 0), 0)
  const capacity = items.reduce((total, account) => total + (account.max_api_keys ?? 0), 0)

  async function startImport() {
    if (!displayName.trim()) {
      setActionError('Enter a display name.')
      return
    }
    setActionError('')
    setBusyID('oauth')
    try {
      const result = await request<{ authorization_url?: string; url?: string }>('/api/v1/provider-accounts/oauth/start', {
        method: 'POST',
        body: JSON.stringify({ display_name: displayName.trim() }),
      })
      const url = result.authorization_url ?? result.url
      if (!url) throw new Error('The server did not return an authorization URL.')
      window.location.assign(url)
    } catch (caught) {
      setActionError(errorMessage(caught))
      setBusyID('')
    }
  }

  async function refreshAccount(id: string) {
    setBusyID(id)
    setActionError('')
    try {
      await request(`/api/v1/provider-accounts/${id}/refresh`, { method: 'POST' })
      await reload()
    } catch (caught) {
      setActionError(errorMessage(caught))
    } finally {
      setBusyID('')
    }
  }

  async function toggleAccount(account: ProviderAccount) {
    const nextStatus = account.status === 'disabled' ? 'active' : 'disabled'
    if (nextStatus === 'disabled' && !window.confirm(`Disable ${account.display_name}? Existing keys will stop routing to it.`)) return
    setBusyID(account.id)
    setActionError('')
    try {
      await request(`/api/v1/provider-accounts/${account.id}`, {
        method: 'PUT',
        body: JSON.stringify({ display_name: account.display_name, status: nextStatus }),
      })
      await reload()
    } catch (caught) {
      setActionError(errorMessage(caught))
    } finally {
      setBusyID('')
    }
  }

  async function removeAccount(account: ProviderAccount) {
    if (!window.confirm(`Remove ${account.display_name}? This permanently deletes its stored OAuth credentials and removes it from every pool.`)) return
    setBusyID(account.id)
    setActionError('')
    try {
      await request(`/api/v1/provider-accounts/${account.id}`, { method: 'DELETE' })
      await reload()
    } catch (caught) {
      setActionError(errorMessage(caught))
    } finally {
      setBusyID('')
    }
  }

  return (
    <section aria-labelledby="accounts-heading">
      <header className="page-heading">
        <div>
          <p className="eyebrow">Upstream capacity</p>
          <h2 id="accounts-heading">Codex accounts</h2>
          <p>Connect Codex subscription accounts. API key capacity is controlled by the global routing setting.</p>
        </div>
        <button className="button button--primary" type="button" onClick={() => setShowImport(true)}>
          <PlusIcon className="button__icon" /> Connect account
        </button>
      </header>
      <div className="metric-strip" aria-label="Account summary">
        <article><span>Healthy</span><strong>{healthy}<small> / {items.length}</small></strong></article>
        <article><span>Key assignments</span><strong>{assigned}<small> / {capacity}</small></strong></article>
        <article><span>Provider</span><strong className="metric-word">CODEX</strong></article>
      </div>
      {actionError ? <div className="inline-alert" role="alert">{actionError}</div> : null}
      {loading ? <StatePanel kind="loading" title="Loading accounts" description="Checking upstream account state and capacity." /> :
        error ? <StatePanel kind="error" title="Accounts unavailable" description={error} actionLabel="Try again" onAction={() => void reload()} /> :
        items.length === 0 ? <StatePanel kind="empty" title="No accounts connected" description="Connect a Codex subscription account before creating a pool." actionLabel="Connect account" onAction={() => setShowImport(true)} /> : (
          <div className="table-frame">
            <table>
              <thead><tr><th>Account</th><th>Status</th><th>API key capacity</th><th>Quota</th><th>Last success</th><th><span className="sr-only">Actions</span></th></tr></thead>
              <tbody>{items.map((account) => {
                const cap = accountCapacity(account)
                return <tr key={account.id}>
                  <td data-label="Account"><strong>{account.display_name}</strong><small>{account.provider} · subscription OAuth</small></td>
                  <td data-label="Status"><span className={`status status--${account.status}`}><i />{statusLabel(account.status)}</span></td>
                  <td data-label="API key capacity">
                    <div className="capacity"><span><strong>{cap.assigned}</strong> of {cap.max} keys</span><div><i style={{ width: `${cap.percent}%` }} /></div></div>
                  </td>
                  <td data-label="Quota">{account.quota_snapshot?.remaining_percent != null ? `${account.quota_snapshot.remaining_percent}% left` : 'Not reported'}</td>
                  <td data-label="Last success">{account.last_success_at ? new Date(account.last_success_at).toLocaleString() : 'Never'}</td>
                  <td><div className="row-actions"><button className="icon-button" type="button" aria-label={`Refresh ${account.display_name}`} onClick={() => void refreshAccount(account.id)} disabled={busyID === account.id}><RefreshIcon className={busyID === account.id ? 'spin' : ''} /></button><button className={`text-button ${account.status === 'disabled' ? '' : 'text-button--danger'}`} type="button" onClick={() => void toggleAccount(account)} disabled={busyID === account.id}>{account.status === 'disabled' ? 'Enable' : 'Disable'}</button><button className="text-button text-button--danger" type="button" onClick={() => void removeAccount(account)} disabled={busyID === account.id}>Remove</button></div></td>
                </tr>
              })}</tbody>
            </table>
          </div>
        )}
      {showImport ? (
        <Modal title="Connect Codex account" description="Subpool will open the Codex authorization flow. Upstream credentials never reach the browser." onClose={() => setShowImport(false)}>
          <div className="form-stack">
            <div className="field">
              <label htmlFor="account-name">Display name <span aria-hidden="true">*</span></label>
              <input id="account-name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder="Primary Codex account" />
            </div>
            {actionError ? <div className="inline-alert" role="alert">{actionError}</div> : null}
            <div className="form-actions"><button className="button button--secondary" type="button" onClick={() => setShowImport(false)}>Cancel</button><button className="button button--primary" type="button" onClick={() => void startImport()} disabled={busyID === 'oauth'}>{busyID === 'oauth' ? 'Starting…' : 'Continue to Codex'}</button></div>
          </div>
        </Modal>
      ) : null}
    </section>
  )
}
