import { useState, type Dispatch, type SetStateAction } from 'react'
import { errorMessage, request } from '../api'
import { ChevronIcon, PlusIcon } from '../components/Icons'
import { Modal } from '../components/Modal'
import { PageSkeleton, PickerSkeleton } from '../components/PageSkeleton'
import { StatePanel } from '../components/StatePanel'
import { Spinner } from '../components/Spinner'
import { useRemoteList } from '../hooks/useRemoteList'
import type { Pool, ProviderAccount } from '../types'

function setAccountSelected(
  setter: Dispatch<SetStateAction<string[]>>,
  accountID: string,
  selected: boolean,
) {
  setter((current) => selected
    ? current.includes(accountID) ? current : [...current, accountID]
    : current.filter((id) => id !== accountID))
}

interface AccountPickerProps {
  accounts: ProviderAccount[]
  selected: string[]
  onChange: (accountID: string, selected: boolean) => void
  legend: string
  description: string
  emptyMessage: string
  error?: string
}

function AccountPicker({ accounts, selected, onChange, legend, description, emptyMessage, error }: AccountPickerProps) {
  const descriptionID = `${legend.toLowerCase().replaceAll(' ', '-')}-description`
  return (
    <fieldset className="account-picker" aria-describedby={descriptionID} aria-invalid={error ? 'true' : undefined}>
      <legend>{legend} <span aria-hidden="true">*</span></legend>
      <p id={descriptionID}>{description}</p>
      {accounts.length ? <div className="account-picker__options">
        {accounts.map((account) => (
          <label className="account-choice" key={account.id}>
            <input
              type="checkbox"
              checked={selected.includes(account.id)}
              onChange={(event) => onChange(account.id, event.target.checked)}
            />
            <span><strong>{account.display_name}</strong><small>{account.provider.replaceAll('_', ' ')} · {account.credential_type === 'api_key' ? 'fallback' : 'primary'} · {account.status.replaceAll('_', ' ')}</small></span>
          </label>
        ))}
      </div> : <div className="account-picker__empty">{emptyMessage}</div>}
      {error ? <span className="field__error" role="alert">{error}</span> : null}
    </fieldset>
  )
}

function PoolList({ pools, onManage }: { pools: Pool[]; onManage: (pool: Pool) => void }) {
  return <div className="pool-list">{pools.map((pool, index) => {
    const accountCount = pool.account_count ?? pool.accounts?.length ?? 0
    return (
      <article className="pool-row" key={pool.id}>
        <span className="pool-row__index">{String(index + 1).padStart(2, '0')}</span>
        <div className="pool-row__main"><div><h3>{pool.name}</h3><span className={`status ${pool.enabled === false ? 'status--disabled' : 'status--active'}`}><i />{pool.enabled === false ? 'disabled' : 'active'}</span></div><p>{pool.provider === 'mixed' ? 'mixed providers' : pool.provider.replaceAll('_', ' ')}</p></div>
        <div className="pool-row__meta"><span>ACCOUNTS<strong>{accountCount}</strong></span><span>MODELS<strong>Any supported</strong></span></div>
        <button className="icon-button" type="button" aria-label={`Manage ${pool.name}`} onClick={() => onManage(pool)}><ChevronIcon /></button>
      </article>
    )
  })}</div>
}

interface CreatePoolDialogProps {
  accounts: ProviderAccount[]
  accountsError: string
  accountsLoading: boolean
  actionError: string
  createAccountsError: string
  createNameError: string
  name: string
  saving: boolean
  selected: string[]
  onClose: () => void
  onGoToAccounts: () => void
  onNameChange: (value: string) => void
  onNameBlur: () => void
  onReloadAccounts: () => void
  onSelect: (accountID: string, selected: boolean) => void
  onSubmit: () => void
}

function CreatePoolDialog(props: CreatePoolDialogProps) {
  return <Modal title="Create account pool" description="Select subscription and paid API accounts. New API keys prefer healthy subscription accounts." onClose={props.onClose}>
    <form className="form-stack" onSubmit={(event) => { event.preventDefault(); props.onSubmit() }} noValidate>
      <div className="field">
        <label htmlFor="pool-name">Pool name <span aria-hidden="true">*</span></label>
        <input id="pool-name" value={props.name} onChange={(event) => props.onNameChange(event.target.value)} onBlur={props.onNameBlur} placeholder="Engineering" aria-invalid={props.createNameError ? 'true' : undefined} aria-describedby={props.createNameError ? 'pool-name-error' : undefined} />
        {props.createNameError ? <span className="field__error" id="pool-name-error">{props.createNameError}</span> : null}
      </div>
      {props.accountsLoading ? <PickerSkeleton /> : props.accountsError ? <div className="inline-alert" role="alert">{props.accountsError}<button type="button" onClick={props.onReloadAccounts}>Try again</button></div> : <AccountPicker accounts={props.accounts} selected={props.selected} onChange={props.onSelect} legend="Provider accounts" description={`${props.selected.length} selected · subscriptions are primary and API accounts are fallback.`} emptyMessage="No active provider accounts are available. Connect an account first." error={props.createAccountsError} />}
      {props.accounts.length === 0 && !props.accountsLoading && !props.accountsError ? <button className="button button--secondary button--wide" type="button" onClick={props.onGoToAccounts}>Go to accounts</button> : null}
      <div className="pool-policy-note"><strong>Routing policy</strong><span>Subscription first · API fallback · least assigned</span></div>
      {props.actionError ? <div className="inline-alert" role="alert">{props.actionError}</div> : null}
      <div className="form-actions"><button className="button button--secondary" type="button" onClick={props.onClose}>Cancel</button><button className="button button--primary" type="submit" disabled={props.saving || props.accountsLoading || props.accounts.length === 0}>{props.saving ? <><Spinner /> Creating…</> : `Create pool${props.selected.length ? ` with ${props.selected.length}` : ''}`}</button></div>
    </form>
  </Modal>
}

interface ManagePoolDialogProps {
  pool: Pool
  accounts: ProviderAccount[]
  candidates: ProviderAccount[]
  actionError: string
  name: string
  saving: boolean
  selected: string[]
  onAttach: () => void
  onClose: () => void
  onNameChange: (value: string) => void
  onSaveName: () => void
  onSelect: (accountID: string, selected: boolean) => void
}

function ManagePoolDialog(props: ManagePoolDialogProps) {
  return <Modal title={`Manage ${props.pool.name}`} description="Rename the pool or attach additional provider accounts." onClose={props.onClose}>
    <div className="form-stack">
      <div className="field"><label htmlFor="manage-pool-name">Pool name <span aria-hidden="true">*</span></label><input id="manage-pool-name" value={props.name} onChange={(event) => props.onNameChange(event.target.value)} /></div>
      <div className="form-actions form-actions--compact"><button className="button button--secondary" type="button" disabled={props.saving} onClick={props.onSaveName}>{props.saving ? <><Spinner /> Saving…</> : 'Save name'}</button></div>
      <div className="member-list" aria-label="Current pool members">
        <span className="member-list__label">Current members</span>
        {props.pool.accounts?.length ? props.pool.accounts.map((membership) => {
          const account = props.accounts.find((item) => item.id === membership.provider_account_id)
          const role = membership.priority === 100 || account?.credential_type === 'api_key' ? 'Fallback' : 'Primary'
          return <div className="member-list__row" key={membership.provider_account_id}><span><strong>{account?.display_name ?? membership.provider_account_id}</strong><small>{membership.enabled === false ? 'Membership disabled' : `${role} · ${account?.status ?? 'Enabled'}`}</small></span><span className={`status ${membership.enabled === false || account?.status === 'disabled' ? 'status--disabled' : 'status--active'}`}><i />{membership.enabled === false ? 'disabled' : 'enabled'}</span></div>
        }) : <p>No accounts attached yet.</p>}
      </div>
      <AccountPicker accounts={props.candidates} selected={props.selected} onChange={props.onSelect} legend="Add accounts" description={`${props.selected.length} selected · existing members remain attached.`} emptyMessage="No additional active accounts are available." />
      {props.actionError ? <div className="inline-alert" role="alert">{props.actionError}</div> : null}
      <div className="form-actions"><button className="button button--secondary" type="button" onClick={props.onClose}>Cancel</button><button className="button button--primary" type="button" disabled={props.saving || props.selected.length === 0} onClick={props.onAttach}>{props.saving ? <><Spinner /> Attaching…</> : `Attach selected${props.selected.length ? ` (${props.selected.length})` : ''}`}</button></div>
    </div>
  </Modal>
}

interface PoolsContentProps {
  pools: Pool[]
  loading: boolean
  loadError: string
  actionError: string
  modalOpen: boolean
  accountTotal: number
  onCreate: () => void
  onManage: (pool: Pool) => void
  onReload: () => void
}

function PoolsContent(props: PoolsContentProps) {
  return <>
    <header className="page-heading">
      <div><h2 id="pools-heading">Account pools</h2><p>Group subscription and paid API accounts behind one stable employee-facing endpoint.</p></div>
      {!props.loading && !props.loadError && props.pools.length > 0 ? <button className="button button--primary" type="button" onClick={props.onCreate}><PlusIcon className="button__icon" /> Create pool</button> : null}
    </header>
    {props.loading ? <PageSkeleton metrics={2} variant="list" /> : <><div className="metric-strip metric-strip--two" aria-label="Pool summary">
      <article><span>Active pools</span><strong>{props.pools.filter((pool) => pool.enabled !== false).length}</strong></article>
      <article><span>Account memberships</span><strong>{props.pools.some((pool) => pool.account_count != null || pool.accounts != null) ? props.accountTotal : '—'}</strong></article>
    </div>
    {props.actionError && !props.modalOpen ? <div className="inline-alert" role="alert">{props.actionError}</div> : null}
    {props.loadError ? <StatePanel kind="error" title="Pools unavailable" description={props.loadError} actionLabel="Try again" onAction={props.onReload} /> : props.pools.length === 0 ? <StatePanel kind="empty" title="No pools configured" description="Create a pool and select one or more healthy provider accounts." actionLabel="Create pool" onAction={props.onCreate} /> : <PoolList pools={props.pools} onManage={props.onManage} />}</>}
  </>
}

export function PoolsPage() {
  const { items, loading, error, reload } = useRemoteList<Pool>('/api/v1/pools', ['pools'])
  const accounts = useRemoteList<ProviderAccount>('/api/v1/provider-accounts', ['provider_accounts', 'accounts'])
  const [showCreate, setShowCreate] = useState(false)
  const [managePool, setManagePool] = useState<Pool | null>(null)
  const [createAccountIDs, setCreateAccountIDs] = useState<string[]>([])
  const [attachAccountIDs, setAttachAccountIDs] = useState<string[]>([])
  const [manageName, setManageName] = useState('')
  const [name, setName] = useState('')
  const [nameTouched, setNameTouched] = useState(false)
  const [saving, setSaving] = useState(false)
  const [actionError, setActionError] = useState('')

  const activeAccounts = accounts.items.filter((account) => account.status === 'active')
  const pageLoading = loading || accounts.loading
  const loadError = error || accounts.error
  const createNameError = nameTouched && !name.trim() ? 'Enter a pool name.' : ''
  const createAccountsError = nameTouched && createAccountIDs.length === 0 ? 'Select at least one account.' : ''

  function openCreate() {
    setActionError('')
    setNameTouched(false)
    setShowCreate(true)
  }

  async function runSaving(action: () => Promise<void>) {
    setSaving(true)
    setActionError('')
    try {
      await action()
    } catch (caught) {
      setActionError(errorMessage(caught))
    } finally {
      setSaving(false)
    }
  }

  async function createPool() {
    setNameTouched(true)
    if (!name.trim() || createAccountIDs.length === 0) return
    await runSaving(async () => {
      await request('/api/v1/pools', {
        method: 'POST',
        body: JSON.stringify({ name: name.trim(), provider_account_ids: createAccountIDs }),
      })
      setShowCreate(false)
      setName('')
      setCreateAccountIDs([])
      setNameTouched(false)
      await reload()
    })
  }

  async function attachAccounts() {
    if (!managePool || attachAccountIDs.length === 0) {
      setActionError('Select at least one account to attach.')
      return
    }
    await runSaving(async () => {
      for (const providerAccountID of attachAccountIDs) {
        await request(`/api/v1/pools/${managePool.id}/accounts`, {
          method: 'POST',
          body: JSON.stringify({ provider_account_id: providerAccountID, weight: 1, enabled: true }),
        })
      }
      setManagePool(null)
      setAttachAccountIDs([])
      await reload()
    })
  }

  async function savePoolSettings() {
    if (!managePool) return
    if (!manageName.trim()) {
      setActionError('Enter a pool name.')
      return
    }
    await runSaving(async () => {
      await request(`/api/v1/pools/${managePool.id}`, {
        method: 'PUT',
        body: JSON.stringify({ name: manageName.trim() }),
      })
      setManagePool(null)
      await reload()
    })
  }

  const accountTotal = items.reduce((sum, pool) => sum + (pool.account_count ?? pool.accounts?.length ?? 0), 0)

  return (
    <section aria-labelledby="pools-heading">
      <PoolsContent pools={items} loading={pageLoading} loadError={loadError} actionError={actionError} modalOpen={showCreate || Boolean(managePool)} accountTotal={accountTotal} onCreate={openCreate} onReload={() => { void reload(); void accounts.reload() }} onManage={(pool) => { setActionError(''); setManagePool(pool); setManageName(pool.name); setAttachAccountIDs([]) }} />
      {showCreate ? <CreatePoolDialog accounts={activeAccounts} accountsError={accounts.error} accountsLoading={accounts.loading} actionError={actionError} createAccountsError={createAccountsError} createNameError={createNameError} name={name} saving={saving} selected={createAccountIDs} onClose={() => setShowCreate(false)} onGoToAccounts={() => { setShowCreate(false); window.location.hash = '#/accounts' }} onNameChange={setName} onNameBlur={() => setNameTouched(true)} onReloadAccounts={() => void accounts.reload()} onSelect={(accountID, selected) => setAccountSelected(setCreateAccountIDs, accountID, selected)} onSubmit={() => void createPool()} /> : null}
      {managePool ? <ManagePoolDialog pool={managePool} accounts={accounts.items} candidates={activeAccounts.filter((account) => !managePool.accounts?.some((membership) => membership.provider_account_id === account.id))} actionError={actionError} name={manageName} saving={saving} selected={attachAccountIDs} onAttach={() => void attachAccounts()} onClose={() => setManagePool(null)} onNameChange={setManageName} onSaveName={() => void savePoolSettings()} onSelect={(accountID, selected) => setAccountSelected(setAttachAccountIDs, accountID, selected)} /> : null}
    </section>
  )
}
