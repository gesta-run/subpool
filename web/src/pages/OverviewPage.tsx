import { useMemo } from 'react'
import { PageSkeleton } from '../components/PageSkeleton'
import { StatePanel } from '../components/StatePanel'
import { useRemoteList } from '../hooks/useRemoteList'
import type { APIKeyRecord, Pool, ProviderAccount, UsageRecord } from '../types'
import './OverviewPage.css'

function compact(value: number) {
  return new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(value)
}

function relativeTime(value?: string | null) {
  if (!value) return 'Never'
  const minutes = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 60_000))
  if (minutes < 1) return 'Just now'
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.round(minutes / 60)
  return hours < 24 ? `${hours}h ago` : `${Math.round(hours / 24)}d ago`
}

function OverviewMetrics({ accounts, pools, keys, input, output }: { accounts: ProviderAccount[]; pools: Pool[]; keys: APIKeyRecord[]; input: number; output: number }) {
  const activeAccounts = accounts.filter((account) => account.status === 'active')
  const assignedKeys = accounts.reduce((sum, account) => sum + (account.assigned_api_keys ?? 0), 0)
  return <div className="metric-strip metric-strip--four" aria-label="Subpool summary">
    <article><span>Active accounts</span><strong>{activeAccounts.length}</strong><small>{accounts.length - activeAccounts.length} need attention</small></article>
    <article><span>Key assignments</span><strong>{assignedKeys}</strong><small>Across {accounts.length} accounts</small></article>
    <article><span>Active API keys</span><strong>{keys.length}</strong><small>{pools.length} routing pools</small></article>
    <article><span>Total tokens</span><strong>{compact(input + output)}</strong><small>{compact(input)} input · {compact(output)} output</small></article>
  </div>
}

function AssignmentsPanel({ accounts, error, onReload }: { accounts: ProviderAccount[]; error: string; onReload: () => void }) {
  return <section className="overview-panel" aria-labelledby="assignments-heading">
    <header><div><h3 id="assignments-heading">Account assignments</h3><p>Employee API keys currently bound to each account</p></div><a href="#/accounts">Manage accounts</a></header>
    {error ? <StatePanel kind="error" title="Accounts unavailable" description={error} actionLabel="Try again" onAction={onReload} /> : accounts.length === 0 ? <StatePanel kind="empty" title="No accounts connected" description="Connect a provider account before creating employee API keys." /> : <div className="capacity-list">
      {accounts.slice(0, 6).map((account) => <article key={account.id}>
        <div><strong>{account.display_name}</strong><small>{account.provider} · {account.status.replaceAll('_', ' ')}</small></div>
        <span className="capacity-count" aria-label={`${account.assigned_api_keys ?? 0} API keys assigned`}>{account.assigned_api_keys ?? 0} keys</span>
      </article>)}
    </div>}
  </section>
}

function UsagePanel({ items, error, onReload }: { items: UsageRecord[]; error: string; onReload: () => void }) {
  return <section className="overview-panel" aria-labelledby="usage-heading-overview">
    <header><div><h3 id="usage-heading-overview">Token usage</h3><p>Input and output totals by API key</p></div><a href="#/usage">View usage</a></header>
    {error ? <StatePanel kind="error" title="Usage unavailable" description={error} actionLabel="Try again" onAction={onReload} /> : items.length === 0 ? <StatePanel kind="empty" title="No token usage yet" description="Usage appears after an employee key completes its first request." /> : <div className="usage-bars">
      {items.map((item) => {
        const total = item.input_tokens + item.output_tokens
        const peak = items[0].input_tokens + items[0].output_tokens
        const width = peak > 0 ? Math.max(4, total / peak * 100) : 0
        return <article key={item.api_key_id}>
          <div><strong>{item.employee_name ?? 'Unassigned'}</strong><span>{compact(total)}</span></div>
          <div className="usage-bar" aria-label={`${total} total tokens`}><i style={{ width: `${width}%` }} /></div>
          <small>{compact(item.input_tokens)} input · {compact(item.output_tokens)} output</small>
        </article>
      })}
    </div>}
  </section>
}

function RecentKeysPanel({ accounts, keys, pools, usageByKey, error, onReload }: { accounts: ProviderAccount[]; keys: APIKeyRecord[]; pools: Pool[]; usageByKey: Map<string, UsageRecord>; error: string; onReload: () => void }) {
  return <section className="overview-panel overview-panel--table" aria-labelledby="recent-keys-heading">
    <header><div><h3 id="recent-keys-heading">Recent key activity</h3><p>Usage totals only; request content is never stored</p></div><a href="#/api-keys">Manage keys</a></header>
    {error ? <StatePanel kind="error" title="API keys unavailable" description={error} actionLabel="Try again" onAction={onReload} /> : keys.length === 0 ? <StatePanel kind="empty" title="No API keys issued" description="Create an employee key after a routing pool is ready." /> : <div className="table-frame"><table><thead><tr><th>Employee</th><th>Pool</th><th>Bound account</th><th className="number">Input</th><th className="number">Output</th><th>Last used</th></tr></thead><tbody>
      {keys.slice(0, 5).map((key) => {
        const usage = usageByKey.get(key.id)
        return <tr key={key.id}>
        <td data-label="Employee"><strong>{key.employee_name}</strong><small><code>••••{key.key_hint}</code></small></td>
        <td data-label="Pool">{key.pool_name ?? pools.find((pool) => pool.id === key.pool_id)?.name ?? key.pool_id}</td>
        <td data-label="Bound account">{accounts.find((account) => account.id === key.provider_account_id)?.display_name ?? 'Pending assignment'}</td>
        <td data-label="Input" className="number">{compact(usage?.input_tokens ?? 0)}</td><td data-label="Output" className="number">{compact(usage?.output_tokens ?? 0)}</td>
        <td data-label="Last used">{relativeTime(key.last_used_at)}</td>
      </tr>})}
    </tbody></table></div>}
  </section>
}

export function OverviewPage() {
  const accounts = useRemoteList<ProviderAccount>('/api/v1/provider-accounts', ['provider_accounts', 'accounts'])
  const pools = useRemoteList<Pool>('/api/v1/pools', ['pools'])
  const keys = useRemoteList<APIKeyRecord>('/api/v1/api-keys', ['api_keys', 'keys'])
  const usage = useRemoteList<UsageRecord>('/api/v1/usage', ['usage', 'records'])

  const summary = useMemo(() => usage.items.reduce((total, item) => ({
    input: total.input + item.input_tokens,
    output: total.output + item.output_tokens,
  }), { input: 0, output: 0 }), [usage.items])

  const usageByKey = useMemo(() => {
    const result = new Map<string, UsageRecord>()
    for (const item of usage.items) {
      const existing = result.get(item.api_key_id)
      if (existing) {
        existing.input_tokens += item.input_tokens
        existing.output_tokens += item.output_tokens
      } else {
        result.set(item.api_key_id, { ...item })
      }
    }
    return result
  }, [usage.items])

  const topUsage = useMemo(() => [...usageByKey.values()]
    .sort((a, b) => b.input_tokens + b.output_tokens - a.input_tokens - a.output_tokens)
    .slice(0, 5), [usageByKey])

  const activeKeys = keys.items.filter((key) => !key.revoked_at)
  const loading = accounts.loading || pools.loading || keys.loading || usage.loading

  return (
    <section aria-labelledby="overview-heading">
      <header className="page-heading">
        <div>
          <h2 id="overview-heading">Overview</h2>
          <p>Account health, assignments, and token usage across connected Codex subscriptions.</p>
        </div>
      </header>

      {loading ? <PageSkeleton metrics={4} variant="dashboard" /> : (
        <>
          <OverviewMetrics accounts={accounts.items} pools={pools.items} keys={activeKeys} input={summary.input} output={summary.output} />
          <div className="overview-grid">
            <AssignmentsPanel accounts={accounts.items} error={accounts.error} onReload={() => void accounts.reload()} />
            <UsagePanel items={topUsage} error={usage.error} onReload={() => void usage.reload()} />
          </div>
          <RecentKeysPanel accounts={accounts.items} keys={activeKeys} pools={pools.items} usageByKey={usageByKey} error={keys.error} onReload={() => void keys.reload()} />
        </>
      )}
    </section>
  )
}
