import { useCallback, useEffect, useMemo, useState } from 'react'
import { collection, errorMessage, request } from '../api'
import { ChevronDownIcon, RefreshIcon } from '../components/Icons'
import { PageSkeleton } from '../components/PageSkeleton'
import { StatePanel } from '../components/StatePanel'
import type { UsageRecord } from '../types'
import './UsagePage.css'

type Range = 'today' | '7d' | '30d' | 'all'

const ranges = [['today', 'Today'], ['7d', '7 days'], ['30d', '30 days'], ['all', 'All time']] as const
const integerFormatter = new Intl.NumberFormat('en-US')

function formatTokens(value: number) {
  if (value < 1_000_000) return integerFormatter.format(value)
  if (value < 1_000_000_000) return `${(value / 1_000_000).toFixed(2)}M`
  return `${(value / 1_000_000_000).toFixed(2)}B`
}

interface UsageGroup extends UsageRecord {
  models: UsageRecord[]
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
  const [expandedKey, setExpandedKey] = useState('')

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
    const byKey = new Map<string, UsageGroup>()
    for (const item of items) {
      let group = byKey.get(item.api_key_id)
      if (!group) {
        group = { ...item, input_tokens: 0, output_tokens: 0, models: [] }
        byKey.set(item.api_key_id, group)
      }
      group.input_tokens += item.input_tokens
      group.output_tokens += item.output_tokens
      const model = item.model || 'unknown'
      const existingModel = group.models.find((entry) => (entry.model || 'unknown') === model)
      if (existingModel) {
        existingModel.input_tokens += item.input_tokens
        existingModel.output_tokens += item.output_tokens
      } else group.models.push({ ...item, model })
    }
    return [...byKey.values()].map((group) => ({ ...group, models: group.models.sort((a, b) => (b.input_tokens + b.output_tokens) - (a.input_tokens + a.output_tokens)) })).sort((a, b) => (b.input_tokens + b.output_tokens) - (a.input_tokens + a.output_tokens))
  }, [items])

  return (
    <section className="usage-page" aria-labelledby="usage-heading">
      <header className="page-heading">
        <div><h2 id="usage-heading">Token usage</h2><p>Input and output totals per API key. Prompts, responses, and request records are never stored.</p></div>
        <div className="usage-range" role="group" aria-label="Usage time range">
          {ranges.map(([value, label]) => <button key={value} type="button" className={range === value ? 'active' : ''} aria-pressed={range === value} disabled={loading} onClick={() => setRange(value)}>{label}</button>)}
        </div>
      </header>
      {!loading ? <><div className="usage-total" aria-label="Token summary">
        <article><span>INPUT TOKENS</span><strong>{formatTokens(summary.input)}</strong><small>Prompt + cached input reported upstream</small></article>
        <i aria-hidden="true" />
        <article><span>OUTPUT TOKENS</span><strong>{formatTokens(summary.output)}</strong><small>Generated output reported upstream</small></article>
        <i aria-hidden="true" />
        <article><span>TOTAL TOKENS</span><strong>{formatTokens(summary.input + summary.output)}</strong><small>Across {grouped.length} API {grouped.length === 1 ? 'key' : 'keys'}</small></article>
      </div>
      {error ? <StatePanel kind="error" title="Usage unavailable" description={error} actionLabel="Try again" onAction={() => void load()} /> :
        grouped.length === 0 ? <StatePanel kind="empty" title="No token usage yet" description="Usage will appear after an employee API key completes its first request." /> : (
          <div className="table-frame usage-table-frame">
            <header className="usage-table-heading"><div><h3>Usage by API key</h3><p>Expand a key to inspect its model totals.</p></div><button className="button button--secondary" type="button" onClick={() => void load()}><RefreshIcon className="button__icon" /> Refresh</button></header>
            <table className="usage-table"><thead><tr><th>Employee</th><th>API key</th><th className="number">Input tokens</th><th className="number">Output tokens</th><th className="number">Total</th></tr></thead>
            <tbody>{grouped.map((item) => {
              const expanded = expandedKey === item.api_key_id
              const detailsID = `usage-models-${item.api_key_id}`
              return [<tr className="usage-key-row" key={item.api_key_id}>
                <td data-label="Employee"><button className="usage-key-toggle" type="button" aria-expanded={expanded} aria-controls={detailsID} onClick={() => setExpandedKey(expanded ? '' : item.api_key_id)}><span className="usage-key-toggle__icon"><ChevronDownIcon /></span><span className="usage-key-toggle__label"><strong>{item.employee_name ?? 'Unassigned'}</strong><small>{item.models.length} {item.models.length === 1 ? 'model' : 'models'}</small></span></button></td>
                <td data-label="API key"><code>••••{item.key_hint ?? item.api_key_id.slice(-4)}</code></td>
                <td data-label="Input tokens" className="number">{formatTokens(item.input_tokens)}</td>
                <td data-label="Output tokens" className="number">{formatTokens(item.output_tokens)}</td>
                <td data-label="Total" className="number"><strong>{formatTokens(item.input_tokens + item.output_tokens)}</strong></td>
              </tr>, expanded ? <tr className="usage-model-detail" key={`${item.api_key_id}-models`}><td colSpan={5}><div className="usage-model-list" id={detailsID} role="region" aria-label={`Model usage for ${item.employee_name ?? 'API key'}`}><div className="usage-model-list__header"><span>Model breakdown</span><span>Input</span><span>Output</span><span>Total</span></div>{item.models.map((model) => <div key={model.model}><span className="usage-model-name"><i /><span><strong>{model.model === 'unknown' ? 'Unattributed' : model.model}</strong>{model.model === 'unknown' ? <small>Historical usage</small> : null}</span></span><span>{formatTokens(model.input_tokens)}</span><span>{formatTokens(model.output_tokens)}</span><strong>{formatTokens(model.input_tokens + model.output_tokens)}</strong></div>)}</div></td></tr> : null]
            })}</tbody></table>
          </div>
        )}</> : <PageSkeleton metrics={3} variant="table" />}
    </section>
  )
}
