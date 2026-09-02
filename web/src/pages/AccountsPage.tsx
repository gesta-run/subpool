import { useEffect, useState } from 'react'
import { AccountTable } from '../components/accounts/AccountTable'
import { ConnectAccountDialog } from '../components/accounts/ConnectAccountDialog'
import { ModelsDialog } from '../components/accounts/ModelsDialog'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { PlusIcon } from '../components/Icons'
import { PageSkeleton } from '../components/PageSkeleton'
import { StatePanel } from '../components/StatePanel'
import { useAccountModels } from '../hooks/useAccountModels'
import { useAccountMutations } from '../hooks/useAccountMutations'
import { useConnectAccount } from '../hooks/useConnectAccount'
import { useRemoteList } from '../hooks/useRemoteList'
import { useResetCredits } from '../hooks/useResetCredits'
import type { ProviderAccount } from '../types'
import './AccountsPage.css'

type PendingAction =
  | { type: 'disable' | 'remove'; account: ProviderAccount }
  | { type: 'reset'; account: ProviderAccount; creditID?: string }

function AccountSummary({ accounts }: { accounts: ProviderAccount[] }) {
  const healthy = accounts.filter((account) => account.health_status === 'healthy').length
  const assigned = accounts.reduce((total, account) => total + (account.assigned_api_keys ?? 0), 0)
  return <dl className="accounts-summary" aria-label="Account summary">
    <div><dt><i className="accounts-summary__health" />Healthy</dt><dd>{healthy}<small> / {accounts.length}</small></dd></div>
    <div title="Employee API keys currently bound to provider accounts"><dt>Bound keys</dt><dd>{assigned}</dd></div>
    <div><dt>Providers</dt><dd>{new Set(accounts.map((account) => account.provider)).size}</dd></div>
  </dl>
}

export function AccountsPage() {
  const list = useRemoteList<ProviderAccount>('/api/v1/provider-accounts', ['provider_accounts', 'accounts'])
  const connect = useConnectAccount(list.reload)
  const mutations = useAccountMutations(list.reload)
  const resets = useResetCredits(list.reload)
  const models = useAccountModels()
  const [pending, setPending] = useState<PendingAction | null>(null)

  useEffect(() => {
    list.items
      .filter((account) => account.provider === 'codex' && account.credential_type !== 'api_key')
      .forEach((account) => void resets.load(account.id))
  }, [list.items])

  function toggle(account: ProviderAccount) {
    if (account.status === 'disabled') void mutations.updateStatus(account, 'active')
    else setPending({ type: 'disable', account })
  }

  function confirmPending() {
    if (!pending) return
    setPending(null)
    if (pending.type === 'remove') void mutations.remove(pending.account)
    else if (pending.type === 'reset') void resets.consume(pending.account, pending.creditID)
    else void mutations.updateStatus(pending.account, 'disabled')
  }

  return <section className="accounts-page" aria-labelledby="accounts-heading">
    <header className="page-heading"><div><h2 id="accounts-heading">Provider accounts</h2><p>Connect and manage subscription or API endpoint accounts. Employee key capacity is controlled in Global settings.</p></div>
      {!list.loading && !list.error && list.items.length > 0 ? <button className="button button--primary" type="button" onClick={() => connect.setOpen(true)}><PlusIcon className="button__icon" /> Connect account</button> : null}
    </header>
    {list.loading ? <PageSkeleton metrics={3} variant="table" /> : <>
      <AccountSummary accounts={list.items} />
      {mutations.error ? <div className="inline-alert" role="alert">{mutations.error}</div> : null}
      {list.error ? <StatePanel kind="error" title="Accounts unavailable" description={list.error} actionLabel="Try again" onAction={() => void list.reload()} /> : list.items.length === 0 ? <StatePanel kind="empty" title="No accounts connected" description="Connect a provider account before creating a pool." actionLabel="Connect account" onAction={() => connect.setOpen(true)} /> : <AccountTable
        accounts={list.items} busyID={mutations.busyID} resetBusyID={resets.busyID} resetStates={resets.states}
        onModels={models.open} onRefresh={(account) => void mutations.refresh(account.id, () => resets.load(account.id, true))}
        onResetLoad={(account) => void resets.load(account.id, true)} onReset={(account, creditID) => setPending({ type: 'reset', account, creditID })}
        onToggle={toggle} onRemove={(account) => setPending({ type: 'remove', account })}
      />}
    </>}
    {connect.open ? <ConnectAccountDialog apiKey={connect.apiKey} baseURL={connect.baseURL} busy={connect.busy} displayName={connect.displayName} deviceLogin={connect.deviceLogin} error={connect.error} fieldErrors={connect.fieldErrors} provider={connect.provider} copyStatus={connect.copyStatus} onAPIKeyChange={connect.setAPIKey} onBaseURLChange={connect.setBaseURL} onClose={connect.close} onContinueToOpenAI={() => void connect.continueToOpenAI()} onDisplayNameChange={connect.setDisplayName} onFieldErrorsChange={connect.setFieldErrors} onProviderChange={connect.changeProvider} onSubmit={() => void connect.submit()} /> : null}
    {models.account ? <ModelsDialog account={models.account} models={models.models} loading={models.loading} error={models.error} onClose={models.close} onReload={() => void models.reload()} /> : null}
    {pending ? <ConfirmDialog
      title={pending.type === 'remove' ? `Remove ${pending.account.display_name}?` : pending.type === 'reset' ? `Reset ${pending.account.display_name} quota?` : `Disable ${pending.account.display_name}?`}
      description={pending.type === 'remove' ? 'This permanently deletes its stored credentials and removes it from every pool.' : pending.type === 'reset' ? 'This consumes one earned Codex reset credit. The action cannot be undone.' : 'Existing API keys will stop routing requests to this account.'}
      confirmLabel={pending.type === 'remove' ? 'Remove account' : pending.type === 'reset' ? 'Use full reset' : 'Disable account'} onCancel={() => setPending(null)} onConfirm={confirmPending}
    /> : null}
  </section>
}
