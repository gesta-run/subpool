import type { ProviderAccount } from '../../../types'

export function AntigravityQuota({ account }: { account: ProviderAccount }) {
  const models = Object.entries(account.quota_snapshot?.models ?? {}).filter(([, model]) => model.quotaInfo)
  if (!models.length) return <div className="subscription-summary__empty"><strong>Model quota unavailable</strong><small>Refresh to check Google Antigravity quota.</small></div>
  models.sort(([left], [right]) => left.localeCompare(right))
  function renderModel([id, model]: typeof models[number]) {
      const quota = model.quotaInfo!
      const percent = Math.round(Math.max(0, Math.min(1, quota.remainingFraction)) * 100)
      const reset = quota.resetTime ? Date.parse(quota.resetTime) : NaN
      return <div className="subscription-summary__empty" key={id}>
        <strong>{model.displayName || id} · {percent}% remaining</strong>
        <small>{Number.isFinite(reset) ? `Resets ${new Date(reset).toLocaleString()}` : 'Reset time not reported'}</small>
      </div>
  }
  return <div className="subscription-capacities" aria-label="Antigravity model quotas">
    {models.slice(0, 2).map(renderModel)}
    {models.length > 2 ? <details><summary>Show {models.length - 2} more models</summary><div className="subscription-capacities">{models.slice(2).map(renderModel)}</div></details> : null}
  </div>
}
