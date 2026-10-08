import { ChevronIcon, FastIcon, PowerIcon, RefreshIcon, TrashIcon } from '../Icons'
import { Spinner } from '../Spinner'
import type { ResetCreditState } from '../../hooks/useResetCredits'
import type { CodexCreditsQuota, CodexResetCredits, CreditsQuota, ProviderAccount, QuotaWindow } from '../../types'

function compactDate(value: Date) {
  return value.toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

function resetCreditExpiryLabel(credits: CodexResetCredits) {
  const expirations = (credits.credits ?? []).filter((credit) => credit.status === 'available' && credit.expires_at).map((credit) => credit.expires_at as number)
  return expirations.length === 0 ? 'Reset expiry not reported' : `Next reset expires ${compactDate(new Date(Math.min(...expirations) * 1000))}`
}

function ResetCreditControl({ state, busy, onRetry, onReset }: { state?: ResetCreditState; busy: boolean; onRetry: () => void; onReset: (creditID?: string) => void }) {
  if (!state || state.loading) return <div className="reset-credit reset-credit--inline reset-credit--loading">
    <div className="reset-credit__action"><div className="reset-credit__state" role="status"><Spinner /><span>Loading reset credits</span></div></div>
  </div>
  if (state.error) return <div className="reset-credit reset-credit--inline reset-credit--error">
    <div className="reset-credit__action"><div><strong>Reset status unavailable</strong><small>Try the account again.</small></div><button className="reset-credit__button" type="button" onClick={onRetry}>Retry</button></div>
  </div>
  if (!state.data) return <div className="reset-credit reset-credit--inline reset-credit--empty">
    <div className="reset-credit__action"><div><strong>Resets unavailable</strong><small>This plan did not report reset credits.</small></div></div>
  </div>
  const credit = state.data.credits?.find((item) => item.status === 'available')
  const count = state.data.available_count
  return <div className={`reset-credit reset-credit--inline ${count > 0 ? 'reset-credit--available' : 'reset-credit--empty'}`}>
    <div className="reset-credit__action">
      <div className="reset-credit__summary"><i aria-hidden="true" /><span className="reset-credit__copy"><strong>{count} {count === 1 ? 'reset' : 'resets'} available</strong><small>{count === 0 ? 'No earned resets available' : resetCreditExpiryLabel(state.data)}</small></span></div>
      {count > 0 ? <button className="reset-credit__button" type="button" onClick={() => onReset(credit?.id)} disabled={busy}>Reset credits</button> : null}
      {state.notice ? <small className="reset-credit__notice" role="status">{state.notice}</small> : null}
    </div>
  </div>
}

function statusLabel(status: ProviderAccount['status']) {
  return status === 'active' ? 'enabled' : status.replaceAll('_', ' ')
}

function healthLabel(status: ProviderAccount['health_status']) {
  return !status || status === 'unknown' ? 'unchecked' : status
}

function accountProviderLabel(account: ProviderAccount) {
  if (account.credential_type === 'api_key') return 'OpenAI-compatible · API key'
  if (account.provider === 'copilot') return `${account.email || 'GitHub identity connected'} · GitHub Copilot subscription`
  return `${account.email || 'Email unavailable'} · Codex subscription`
}

function lastCheckedLabel(value: string) {
  const checkedAt = new Date(value)
  const now = new Date()
  const sameDay = checkedAt.getFullYear() === now.getFullYear() && checkedAt.getMonth() === now.getMonth() && checkedAt.getDate() === now.getDate()
  const time = checkedAt.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
  return sameDay ? `Today · ${time}` : `${checkedAt.toLocaleDateString([], { month: 'short', day: 'numeric' })} · ${time}`
}

function CapacityMeter({ accountName, label, window, credits, usageBlocked, quotaIsLastKnown }: { accountName: string; label: string; window: QuotaWindow; credits?: CodexCreditsQuota; usageBlocked: boolean; quotaIsLastKnown: boolean }) {
  const remaining = Math.round(Math.max(0, Math.min(100, window.remaining_percent)))
  const capacityLabel = usageBlocked ? `reported ${label} capacity` : quotaIsLastKnown ? `last known ${label} capacity` : `${label} capacity`
  return <div className="capacity">
    <span className="capacity__heading"><span className="capacity__label"><strong>{remaining}%</strong> {capacityLabel}</span>{credits ? <CodexCreditsInline credits={credits} /> : null}</span>
    <div role="progressbar" aria-label={`${accountName} ${label} usage remaining`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={remaining}><i style={{ width: `${remaining}%` }} /></div>
    <small>{window.reset_at ? `Resets ${compactDate(new Date(window.reset_at * 1000))}` : 'Reset time unavailable'}</small>
  </div>
}

function creditCount(value: number) {
  return new Intl.NumberFormat([], { maximumFractionDigits: 2 }).format(value)
}

function codexCreditBalance(value: string | null) {
  if (!value?.trim()) return undefined
  const balance = Number(value)
  return Number.isFinite(balance) && balance >= 0 ? new Intl.NumberFormat([], { maximumFractionDigits: 0 }).format(balance) : undefined
}

function CodexCreditsInline({ credits }: { credits: CodexCreditsQuota }) {
  const balance = codexCreditBalance(credits.balance)
  return <span className="capacity__credits">{' with '}<strong>{credits.unlimited ? 'Unlimited' : balance ?? 'Available'}</strong> AI credits</span>
}

function CopilotCreditsMeter({ accountName, credits, quotaIsLastKnown }: { accountName: string; credits: CreditsQuota; quotaIsLastKnown: boolean }) {
  const remaining = Math.round(Math.max(0, Math.min(100, credits.remaining_percent)))
  const prefix = quotaIsLastKnown ? 'Last known · ' : ''
  if (credits.unlimited) return <div className="capacity"><span><strong>Unlimited</strong> AI credits</span><small>{prefix}No included credit limit reported</small></div>
  return <div className="capacity">
    <span><strong>{creditCount(credits.remaining)} / {creditCount(credits.entitlement)}</strong> AI credits</span>
    <div role="progressbar" aria-label={`${accountName} AI credits remaining`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={remaining}><i style={{ width: `${remaining}%` }} /></div>
    <small>{prefix}{credits.reset_at ? `Resets ${compactDate(new Date(credits.reset_at * 1000))}` : `${creditCount(credits.remaining)} included credits remaining`}</small>
    <small>{credits.overage_permitted ? `${creditCount(credits.overage_count)} additional credits used` : 'Additional usage disabled'}</small>
  </div>
}

function CodexCreditsBalance({ credits, quotaIsLastKnown }: { credits: CodexCreditsQuota; quotaIsLastKnown: boolean }) {
  const prefix = quotaIsLastKnown ? 'Last known · ' : ''
  if (credits.unlimited) return <div className="capacity"><span><strong>Unlimited</strong> AI credits</span><small>{prefix}No credit limit reported</small></div>
  const balance = codexCreditBalance(credits.balance)
  return <div className="capacity">
    <span><strong>{balance ?? 'Available'}</strong> AI credits</span>
    <small>{prefix}{balance ? 'Available credit balance' : 'Credit balance not reported'}</small>
  </div>
}

function isCopilotCredits(credits: CreditsQuota | CodexCreditsQuota | undefined): credits is CreditsQuota {
  return Boolean(credits && 'remaining' in credits)
}

function isCodexCredits(credits: CreditsQuota | CodexCreditsQuota | undefined): credits is CodexCreditsQuota {
  return Boolean(credits && 'has_credits' in credits)
}

export function AccountTable({ accounts, busyID, resetBusyID, resetStates, onModels, onRefresh, onResetLoad, onReset, onFastMode, onToggle, onRemove }: { accounts: ProviderAccount[]; busyID: string; resetBusyID: string; resetStates: Record<string, ResetCreditState>; onModels: (account: ProviderAccount) => void; onRefresh: (account: ProviderAccount) => void; onResetLoad: (account: ProviderAccount) => void; onReset: (account: ProviderAccount, creditID?: string) => void; onFastMode: (account: ProviderAccount) => void; onToggle: (account: ProviderAccount) => void; onRemove: (account: ProviderAccount) => void }) {
  return <div className="table-frame account-table-frame"><table className="account-table">
    <caption className="sr-only">Connected provider accounts, health, subscription capacity, credits, and actions</caption>
    <colgroup><col className="account-table__account" /><col className="account-table__health" /><col className="account-table__subscription" /><col className="account-table__actions" /></colgroup>
    <thead><tr><th>Account</th><th>Health</th><th>Subscription &amp; credits</th><th>Actions</th></tr></thead>
    <tbody>{accounts.map((account) => {
      const fiveHour = account.quota_snapshot?.five_hour
      const weekly = account.quota_snapshot?.weekly
      const quotaCredits = account.quota_snapshot?.credits
      const copilotCredits = account.provider === 'copilot' && isCopilotCredits(quotaCredits) ? quotaCredits : undefined
      const codexCredits = account.provider === 'codex' && isCodexCredits(quotaCredits) && (quotaCredits.has_credits || quotaCredits.unlimited) ? quotaCredits : undefined
      const usageBlocked = account.status === 'exhausted' || account.quota_snapshot?.usage_allowed === false
      const busy = busyID === account.id || resetBusyID === account.id
      const health = account.health_status ?? 'unknown'
      const effectiveStatus = usageBlocked && account.status === 'active' ? 'exhausted' : account.status
      const unavailable = effectiveStatus !== 'active'
      const degraded = !unavailable && health !== 'unhealthy' && (account.consecutive_health_failures ?? 0) > 0
      const healthStatus = degraded ? 'unknown' : health
      const quotaIsLastKnown = !account.quota_checked_at || Boolean(account.last_quota_error_code)
      const availabilityLabel = unavailable ? statusLabel(effectiveStatus) : degraded ? 'degraded' : healthLabel(health)
      const routingLabel = usageBlocked ? 'Routing suspended' : unavailable ? `${healthLabel(health)} health` : health === 'unhealthy' ? 'Routing suspended' : degraded ? 'Routing enabled while retrying' : 'Routing enabled'
      const isCodexSubscription = account.provider === 'codex' && account.credential_type !== 'api_key'
      const hasLimitWindow = Boolean(fiveHour || weekly)
      return <tr key={account.id}>
        <td data-label="Account"><button className="account-detail-button" type="button" aria-label={`View supported models for ${account.display_name}`} onClick={() => onModels(account)}><span className="account-detail-button__avatar" aria-hidden="true">{account.display_name.trim().charAt(0).toUpperCase() || '?'}</span><span className="account-detail-button__copy"><span className="account-detail-button__title"><strong>{account.display_name}</strong><ChevronIcon /></span><small>{accountProviderLabel(account)}</small></span></button></td>
        <td data-label="Health"><div className="account-health"><span className={`status ${unavailable ? `status--${effectiveStatus}` : `status--health-${healthStatus}`}`}><i />{availabilityLabel}</span><small>{routingLabel}</small><small className="account-health__checked" title={account.last_checked_at ? new Date(account.last_checked_at).toLocaleString() : undefined}>{account.last_checked_at ? `Checked ${lastCheckedLabel(account.last_checked_at)}` : 'Not checked yet'}</small>{account.last_health_error_code ? <small className="health-error">{account.last_health_error_code.replaceAll('_', ' ')}</small> : null}</div></td>
        <td data-label="Subscription and credits"><div className="subscription-summary">{copilotCredits ? <CopilotCreditsMeter accountName={account.display_name} credits={copilotCredits} quotaIsLastKnown={quotaIsLastKnown} /> : <>{hasLimitWindow ? <div className="subscription-capacities">{fiveHour ? <CapacityMeter accountName={account.display_name} label="5-hour" window={fiveHour} credits={!weekly ? codexCredits : undefined} usageBlocked={usageBlocked} quotaIsLastKnown={quotaIsLastKnown} /> : null}{weekly ? <CapacityMeter accountName={account.display_name} label="weekly" window={weekly} credits={codexCredits} usageBlocked={usageBlocked} quotaIsLastKnown={quotaIsLastKnown} /> : null}</div> : null}{codexCredits && !hasLimitWindow ? <CodexCreditsBalance credits={codexCredits} quotaIsLastKnown={quotaIsLastKnown} /> : null}{codexCredits || isCodexSubscription ? <ResetCreditControl state={resetStates[account.id]} busy={busy} onRetry={() => onResetLoad(account)} onReset={(creditID) => onReset(account, creditID)} /> : null}{!hasLimitWindow && !codexCredits ? <div className="subscription-summary__empty"><strong>{account.credential_type === 'api_key' ? 'External API' : account.provider === 'copilot' ? 'Credits unavailable' : 'Usage unavailable'}</strong><small>{account.credential_type === 'api_key' ? 'Quota is managed upstream' : account.provider === 'copilot' ? 'No AI credit quota reported' : 'No subscription quota reported'}</small></div> : null}</>}</div></td>
        <td><div className="row-actions">{account.provider === 'codex' && account.credential_type !== 'api_key' ? <button className={`row-action-button ${account.fast_mode_enabled ? 'row-action-button--active' : ''}`} type="button" aria-label={`${account.fast_mode_enabled ? 'Disable' : 'Enable'} Fast mode for ${account.display_name}`} aria-pressed={Boolean(account.fast_mode_enabled)} title={account.fast_mode_enabled ? 'Disable Fast mode' : 'Enable Fast mode'} onClick={() => onFastMode(account)} disabled={busy}><FastIcon /></button> : null}<button className="row-action-button" type="button" aria-label={`${account.provider === 'codex' && account.credential_type !== 'api_key' ? 'Refresh credentials and quota' : account.provider === 'copilot' ? 'Refresh AI credits' : 'Check health'} for ${account.display_name}`} title={account.provider === 'codex' && account.credential_type !== 'api_key' ? 'Refresh credentials and quota' : account.provider === 'copilot' ? 'Refresh AI credits' : 'Check health'} onClick={() => onRefresh(account)} disabled={busy}>{busyID === account.id ? <Spinner /> : <RefreshIcon />}</button><button className="row-action-button" type="button" aria-label={`${account.status === 'disabled' ? 'Enable' : 'Disable'} ${account.display_name}`} title={account.status === 'disabled' ? 'Enable account' : 'Disable account'} onClick={() => onToggle(account)} disabled={busy}><PowerIcon /></button><button className="row-action-button row-action-button--danger" type="button" aria-label={`Remove ${account.display_name}`} title="Remove account" onClick={() => onRemove(account)} disabled={busy}><TrashIcon /></button></div></td>
      </tr>
    })}</tbody>
  </table></div>
}
