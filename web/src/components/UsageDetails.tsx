import { useEffect, useState } from 'react'
import { errorMessage, request } from '../api'
import type { UsageDetailsResponse, UsageRecord } from '../types'

const integerFormatter = new Intl.NumberFormat('en-US')

function formatTokens(value: number) {
  if (value < 1_000_000) return integerFormatter.format(value)
  if (value < 1_000_000_000) return `${(value / 1_000_000).toFixed(2)}M`
  return `${(value / 1_000_000_000).toFixed(2)}B`
}

export function UsageDetails({ employee, id, path }: { employee: string; id: string; path: string }) {
  const [items, setItems] = useState<UsageRecord[]>([])
  const [nextCursor, setNextCursor] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [reloadKey, setReloadKey] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    void request<UsageDetailsResponse>(path, { signal: controller.signal }).then((data) => {
      setItems(data.items)
      setNextCursor(data.next_cursor)
    }).catch((caught) => {
      if (!controller.signal.aborted) setError(errorMessage(caught))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [path, reloadKey])

  async function loadMore() {
    if (!nextCursor || loading) return
    setLoading(true)
    setError('')
    try {
      const data = await request<UsageDetailsResponse>(`${path}&cursor=${encodeURIComponent(nextCursor)}`)
      setItems((current) => [...current, ...data.items])
      setNextCursor(data.next_cursor)
    } catch (caught) {
      setError(errorMessage(caught))
    } finally {
      setLoading(false)
    }
  }

  return <div className="usage-detail-region" id={id} role="region" aria-label={`Usage details for ${employee}`} aria-busy={loading}>
    <div className="usage-detail-list">
      <div className="usage-detail-list__header"><span>API key</span><span>Model</span><span>Input</span><span>Output</span><span>Total</span></div>
      {items.map((detail) => <div key={`${detail.api_key_id}:${detail.model}`}><code>••••{detail.key_hint || detail.api_key_id.slice(-4)}</code><span className="usage-model-cell"><strong>{detail.model === 'unknown' ? 'Unattributed' : detail.model}</strong>{detail.model === 'unknown' ? <small>Historical usage</small> : null}</span><span>{formatTokens(detail.input_tokens)}</span><span>{formatTokens(detail.output_tokens)}</span><strong>{formatTokens(detail.input_tokens + detail.output_tokens)}</strong></div>)}
      {loading && items.length === 0 ? <p className="usage-detail-state">Loading details…</p> : null}
      {error ? <p className="usage-detail-state" role="alert">{error} <button type="button" onClick={() => setReloadKey((value) => value + 1)}>Try again</button></p> : null}
      {!loading && !error && items.length === 0 ? <p className="usage-detail-state">No details in this range.</p> : null}
    </div>
    {nextCursor ? <button className="button button--secondary usage-detail-more" type="button" disabled={loading} onClick={() => void loadMore()}>{loading ? 'Loading…' : 'Load more'}</button> : null}
  </div>
}
