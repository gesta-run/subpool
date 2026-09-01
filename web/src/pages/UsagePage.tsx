import { useCallback, useEffect, useMemo, useState } from 'react'
import { collection, errorMessage, request } from '../api'
import { RefreshIcon } from '../components/Icons'
import { StatePanel } from '../components/StatePanel'
import type { UsageRecord } from '../types'

type Range = 'today' | '7d' | '30d' | 'all'

function formatNumber(value: number) {
  return new Intl.NumberFormat('en-US').format(value)
}

function localDate(value: Date) {
  const offset = value.getTimezoneOffset() * 60_000
  return new Date(value.getTime() - offset).toISOString().slice(0, 10)
}

function rangeQuery(range: Range) {
  if (range === 'all') return ''
  const to = new Date()
  const from = new Date(to)
  if (range === '7d') from.setDate(from.getDate() - 6)
  if (range === '30d') from.setDate(from.getDate() - 29)
  return `?from=${localDate(from)}&to=${localDate(to)}`
}

export function UsagePage() {
  const [range, setRange] = useState<Range>('7d')
  const [items, setItems] = useState<UsageRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const payload = await request<unknown>(`/api/v1/usage${rangeQuery(range)}`)
      setItems(collection<UsageRecord>(payload, ['usage', 'records']))
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setLoading(false)
    }
  }, [range])

  useEffect(() => { void load() }, [load])

  const summary = useMemo(() => items.reduce((result, item) => ({
    input: result.input + item.input_tokens,
    output: result.output + item.output_tokens,
  }), { input: 0, output: 0 }), [items])

  const grouped = useMemo(() => {
    const byKey = new Map<string, UsageRecord>()
    for (const item of items) {
      const existing = byKey.get(item.api_key_id)
      if (existing) {
        existing.input_tokens += item.input_tokens
        existing.output_tokens += item.output_tokens
      } else {
        byKey.set(item.api_key_id, { ...item })
      }
    }
    return [...byKey.values()].sort((a, b) => (b.input_tokens + b.output_tokens) - (a.input_tokens + a.output_tokens))
  }, [items])

  return (
    <section aria-labelledby="usage-heading">
      <header className="page-heading">
        <div><p className="eyebrow">Aggregate metering</p><h2 id="usage-heading">Token usage</h2><p>Input and output totals per API key. Prompts, responses, and request records are never stored.</p></div>
        <button className="button button--secondary" type="button" onClick={() => void load()} disabled={loading}><RefreshIcon className={`button__icon ${loading ? 'spin' : ''}`} /> Refresh</button>
      </header>
      <div className="filter-bar">
        <span>Time range</span>
        <div role="group" aria-label="Usage time range">
          {([['today', 'Today'], ['7d', '7 days'], ['30d', '30 days'], ['all', 'All time']] as const).map(([value, label]) => <button key={value} type="button" className={range === value ? 'active' : ''} aria-pressed={range === value} onClick={() => setRange(value)}>{label}</button>)}
        </div>
      </div>
      <div className="usage-total" aria-label="Token summary">
        <article><span>INPUT TOKENS</span><strong>{formatNumber(summary.input)}</strong><small>Prompt + cached input reported upstream</small></article>
        <i aria-hidden="true" />
        <article><span>OUTPUT TOKENS</span><strong>{formatNumber(summary.output)}</strong><small>Generated output reported upstream</small></article>
        <i aria-hidden="true" />
        <article><span>TOTAL TOKENS</span><strong>{formatNumber(summary.input + summary.output)}</strong><small>No request content retained</small></article>
      </div>
      {loading ? <StatePanel kind="loading" title="Loading usage" description="Aggregating daily token counters by API key." /> :
        error ? <StatePanel kind="error" title="Usage unavailable" description={error} actionLabel="Try again" onAction={() => void load()} /> :
        grouped.length === 0 ? <StatePanel kind="empty" title="No token usage yet" description="Usage will appear after an employee API key completes its first request." /> : (
          <div className="table-frame"><table><thead><tr><th>Employee</th><th>API key</th><th className="number">Input tokens</th><th className="number">Output tokens</th><th className="number">Total</th></tr></thead>
            <tbody>{grouped.map((item) => <tr key={item.api_key_id}>
              <td data-label="Employee"><strong>{item.employee_name ?? 'Unassigned'}</strong></td>
              <td data-label="API key"><code>••••{item.key_hint ?? item.api_key_id.slice(-4)}</code></td>
              <td data-label="Input tokens" className="number">{formatNumber(item.input_tokens)}</td>
              <td data-label="Output tokens" className="number">{formatNumber(item.output_tokens)}</td>
              <td data-label="Total" className="number"><strong>{formatNumber(item.input_tokens + item.output_tokens)}</strong></td>
            </tr>)}</tbody></table></div>
        )}
    </section>
  )
}
