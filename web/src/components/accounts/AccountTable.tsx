import { ChevronIcon, FastIcon, PowerIcon, RefreshIcon, TrashIcon } from '../Icons'
import { Spinner } from '../Spinner'
import type { ResetCreditState } from '../../hooks/useResetCredits'
import type { CodexResetCredits, ProviderAccount } from '../../types'

function compactDate(value: Date) {
  return value.toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

function resetCreditExpiryLabel(credits: CodexResetCredits) {
  const expirations = (credits.credits ?? []).filter((credit) => credit.status === 'available' && credit.expires_at).map((credit) => credit.expires_at as number)
  return expirations.length === 0 ? 'Expiry not reported' : `Expires ${compactDate(new Date(Math.min(...expirations) * 1000))}`
}

function ResetCreditControl({ state, busy, onRetry, onReset }: { state?: ResetCreditState; busy: boolean; onRetry: () => void; onReset: (creditID?: string) => void }) {
  if (!state) return <div className="reset-credit reset-credit--inline reset-credit--loading" role="status"><Spinner /><span>Loading reset credits</span></div>
  if (state.loading) return <div className="reset-credit reset-credit--inline reset-credit--loading" role="status"><Spinner /><span>Loading reset credits</span></div>
  if (state.error) return <div className="reset-credit reset-credit--inline reset-credit--error"><div><strong>Reset status unavailable</strong><small>Try the account again.</small></div><button className="reset-credit__button" type="button" onClick={onRetry}>Retry</button></div>
  if (!state.data) return <div className="reset-credit reset-credit--inline reset-credit--empty"><div><strong>Resets unavailable</strong><small>This plan did not report reset credits.</small></div></div>
  const credit = state.data.credits?.find((item) => item.status === 'available')
  const count = state.data.available_count
  return <div className={`reset-credit reset-credit--inline ${count > 0 ? 'reset-credit--available' : 'reset-credit--empty'}`}>
    <div className="reset-credit__summary"><i aria-hidden="true" /><span className="reset-credit__copy"><strong>{count} {count === 1 ? 'reset' : 'resets'} available</strong><small>{count > 0 ? resetCreditExpiryLabel(state.data) : 'No earned resets available'}</small></span></div>
    {state.notice ? <small className="reset-credit__notice" role="status">{state.notice}</small> : null}
    {count > 0 ? <button className="reset-credit__button" type="button" onClick={() => onReset(credit?.id)} disabled={busy}>Reset quota</button> : null}
  </div>
}

function statusLabel(status: ProviderAccount['status']) {
  return status === 'active' ? 'enabled' : status.replaceAll('_', ' ')
}

function healthLabel(status: ProviderAccount['health_status']) {
  return !status || status === 'unknown' ? 'unchecked' : status
}

function lastCheckedLabel(value: string) {
  const checkedAt = new Date(value)
  const now = new Date()
  const sameDay = checkedAt.getFullYear() === now.getFullYear() && checkedAt.getMonth() === now.getMonth() && checkedAt.getDate() === now.getDate()
  const time = checkedAt.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
  return sameDay ? `Today · ${time}` : `${checkedAt.toLocaleDateString([], { month: 'short', day: 'numeric' })} · ${time}`
}

export function AccountTable({ accounts, busyID, resetBusyID, resetStates, onModels, onRefresh, onResetLoad, onReset, onFastMode, onToggle, onRemove }: { accounts: ProviderAccount[]; busyID: string; resetBusyID: string; resetStates: Record<string, ResetCreditState>; onModels: (account: ProviderAccount) => void; onRefresh: (account: ProviderAccount) => void; onResetLoad: (account: ProviderAccount) => void; onReset: (account: ProviderAccount, creditID?: string) => void; onFastMode: (account: ProviderAccount) => void; onToggle: (account: ProviderAccount) => void; onRemove: (account: ProviderAccount) => void }) {
  return <div className="table-frame account-table-frame"><table className="account-table">
    <caption className="sr-only">Connected provider accounts, routing availability, subscription capacity, and actions</caption>
    <colgroup><col className="account-table__account" /><col className="account-table__availability" /><col className="account-table__subscription" /><col className="account-table__checked" /><col className="account-table__actions" /></colgroup>
    <thead><tr><th>Account</th><th>Availability</th><th>Subscription</th><th>Last checked</th><th><span className="sr-only">Actions</span></th></tr></thead>
    <tbody>{accounts.map((account) => {
      const weekly = account.quota_snapshot?.weekly
      const remaining = weekly ? Math.round(Math.max(0, Math.min(100, weekly.remaining_percent))) : null
      const busy = busyID === account.id || resetBusyID === account.id
      const health = account.health_status ?? 'unknown'
      const unavailable = account.status !== 'active'
      const degraded = !unavailable && health !== 'unhealthy' && (account.consecutive_health_failures ?? 0) > 0
      const healthStatus = degraded ? 'unknown' : health
      const availabilityLabel = unavailable ? statusLabel(account.status) : degraded ? 'degraded' : healthLabel(health)
      const routingLabel = unavailable ? `${healthLabel(health)} health` : health === 'unhealthy' ? 'Routing suspended' : degraded ? 'Routing enabled while retrying' : 'Routing enabled'
      const capacityLabel = degraded || health === 'unhealthy' ? 'last known weekly capacity' : 'weekly capacity'
      return <tr key={account.id}>
        <td data-label="Account"><button className="account-detail-button" type="button" aria-label={`View supported models for ${account.display_name}`} onClick={() => onModels(account)}><span className="account-detail-button__title"><strong>{account.display_name}</strong><ChevronIcon /></span><small>{account.credential_type === 'api_key' ? 'OpenAI-compatible · API key' : `${account.email || 'Email unavailable'} · Codex subscription`}</small></button></td>
        <td data-label="Availability"><div className="account-availability"><span className={`status ${unavailable ? `status--${account.status}` : `status--health-${healthStatus}`}`}><i />{availabilityLabel}</span><small>{routingLabel}</small>{account.last_health_error_code ? <small className="health-error">{account.last_health_error_code.replaceAll('_', ' ')}</small> : null}</div></td>
        <td data-label="Subscription"><div className="subscription-summary">{weekly && remaining != null ? <div className="capacity"><span><strong>{remaining}%</strong> {capacityLabel}</span><div role="progressbar" aria-label={`${account.display_name} weekly usage remaining`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={remaining}><i style={{ width: `${remaining}%` }} /></div><small>{weekly.reset_at ? `Resets ${compactDate(new Date(weekly.reset_at * 1000))}` : 'Reset time unavailable'}</small></div> : <div className="subscription-summary__empty"><strong>{account.credential_type === 'api_key' ? 'External API' : 'Usage unavailable'}</strong><small>{account.credential_type === 'api_key' ? 'Quota is managed upstream' : 'No weekly quota reported'}</small></div>}{account.provider === 'codex' && account.credential_type !== 'api_key' ? <ResetCreditControl state={resetStates[account.id]} busy={busy} onRetry={() => onResetLoad(account)} onReset={(creditID) => onReset(account, creditID)} /> : null}</div></td>
        <td data-label="Last checked"><div className="last-checked" title={account.last_checked_at ? new Date(account.last_checked_at).toLocaleString() : undefined}><strong>{account.last_checked_at ? lastCheckedLabel(account.last_checked_at) : 'Never'}</strong><small>Health probe</small></div></td>
        <td><div className="row-actions">{account.provider === 'codex' && account.credential_type !== 'api_key' ? <><button className={`row-action-button ${account.fast_mode_enabled ? 'row-action-button--active' : ''}`} type="button" aria-label={`${account.fast_mode_enabled ? 'Disable' : 'Enable'} Fast mode for ${account.display_name}`} aria-pressed={Boolean(account.fast_mode_enabled)} title={account.fast_mode_enabled ? 'Disable Fast mode' : 'Enable Fast mode'} onClick={() => onFastMode(account)} disabled={busy}><FastIcon /></button><button className="row-action-button" type="button" aria-label={`Refresh credentials for ${account.display_name}`} title="Refresh credentials" onClick={() => onRefresh(account)} disabled={busy}>{busyID === account.id ? <Spinner /> : <RefreshIcon />}</button></> : null}<button className="row-action-button" type="button" aria-label={`${account.status === 'disabled' ? 'Enable' : 'Disable'} ${account.display_name}`} title={account.status === 'disabled' ? 'Enable account' : 'Disable account'} onClick={() => onToggle(account)} disabled={busy}><PowerIcon /></button><button className="row-action-button row-action-button--danger" type="button" aria-label={`Remove ${account.display_name}`} title="Remove account" onClick={() => onRemove(account)} disabled={busy}><TrashIcon /></button></div></td>
      </tr>
    })}</tbody>
  </table></div>
}
