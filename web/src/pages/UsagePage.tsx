import { Fragment, useMemo, useState } from 'react'
import { ChevronDownIcon, RefreshIcon } from '../components/Icons'
import { PageSkeleton } from '../components/PageSkeleton'
import { StatePanel } from '../components/StatePanel'
import { UsageDetails } from '../components/UsageDetails'
import { useUsagePage } from '../hooks/useUsagePage'
import './UsagePage.css'

type Range = 'today' | '7d' | '30d' | 'all'

const ranges = [['today', 'Today'], ['7d', '7 days'], ['30d', '30 days'], ['all', 'All time']] as const
const integerFormatter = new Intl.NumberFormat('en-US')
const pageSize = 50

function formatTokens(value: number) {
  if (value < 1_000_000) return integerFormatter.format(value)
  if (value < 1_000_000_000) return `${(value / 1_000_000).toFixed(2)}M`
  return `${(value / 1_000_000_000).toFixed(2)}B`
}

function localDate(value: Date) {
  const offset = value.getTimezoneOffset() * 60_000
  return new Date(value.getTime() - offset).toISOString().slice(0, 10)
}

function usagePath(range: Range, cursor: string) {
  const query = new URLSearchParams({ limit: String(pageSize) })
  if (range !== 'all') {
    const to = new Date()
    const from = new Date(to)
    if (range === '7d') from.setDate(from.getDate() - 6)
    if (range === '30d') from.setDate(from.getDate() - 29)
    query.set('from', localDate(from))
    query.set('to', localDate(to))
  }
  if (cursor) query.set('cursor', cursor)
  return `/api/v1/usage?${query.toString()}`
}

function usageDetailsPath(employeeID: string, range: Range) {
  const query = new URLSearchParams({ limit: String(pageSize) })
  if (range !== 'all') {
    const to = new Date()
    const from = new Date(to)
    if (range === '7d') from.setDate(from.getDate() - 6)
    if (range === '30d') from.setDate(from.getDate() - 29)
    query.set('from', localDate(from))
    query.set('to', localDate(to))
  }
  return `/api/v1/usage/employees/${employeeID}/details?${query.toString()}`
}

export function UsagePage() {
  const [range, setRange] = useState<Range>('7d')
  const [pageIndex, setPageIndex] = useState(0)
  const [cursors, setCursors] = useState([''])
  const [expandedEmployeeID, setExpandedEmployeeID] = useState('')
  const path = useMemo(() => usagePath(range, cursors[pageIndex] ?? ''), [range, cursors, pageIndex])
  const { data, loading, error, reload } = useUsagePage(path)

  const selectRange = (value: Range) => {
    setRange(value)
    setPageIndex(0)
    setCursors([''])
    setExpandedEmployeeID('')
  }

  const nextPage = () => {
    if (!data?.next_cursor) return
    setCursors((current) => [...current.slice(0, pageIndex + 1), data.next_cursor])
    setPageIndex((current) => current + 1)
    setExpandedEmployeeID('')
  }

  const previousPage = () => {
    setPageIndex((current) => Math.max(0, current - 1))
    setExpandedEmployeeID('')
  }

  return (
    <section className="usage-page" aria-labelledby="usage-heading" aria-busy={loading}>
      <header className="page-heading">
        <div><h2 id="usage-heading">Token usage</h2><p>Aggregated token totals only. Prompts, responses, and individual request records are never stored.</p></div>
        <div className="usage-range" role="group" aria-label="Usage time range">
          {ranges.map(([value, label]) => <button key={value} type="button" className={range === value ? 'active' : ''} aria-pressed={range === value} disabled={loading} onClick={() => selectRange(value)}>{label}</button>)}
        </div>
      </header>

      {!data && loading ? <PageSkeleton metrics={3} variant="table" /> : !data && error ? <StatePanel kind="error" title="Usage unavailable" description={error} actionLabel="Try again" onAction={() => void reload()} /> : data ? <>
        <div className="usage-total" aria-label="Token summary">
          <article><span>INPUT TOKENS</span><strong>{formatTokens(data.summary.input_tokens)}</strong><small>Prompt + cached input reported upstream</small></article>
          <i aria-hidden="true" />
          <article><span>OUTPUT TOKENS</span><strong>{formatTokens(data.summary.output_tokens)}</strong><small>Generated output reported upstream</small></article>
          <i aria-hidden="true" />
          <article><span>TOTAL TOKENS</span><strong>{formatTokens(data.summary.input_tokens + data.summary.output_tokens)}</strong><small>Across the selected time range</small></article>
        </div>

        {error ? <div className="usage-feedback" role="alert"><span>{error}</span><button type="button" onClick={() => void reload()}>Try again</button></div> : null}
        {data.items.length === 0 && pageIndex === 0 ? <StatePanel kind="empty" title="No token usage yet" description="Usage will appear after an employee API key completes its first request." /> : (
          <div className="table-frame usage-table-frame">
            <header className="usage-table-heading">
              <div><h3>Usage by employee</h3><p>Expand an employee to inspect model totals.</p></div>
              <button className="button button--secondary" type="button" disabled={loading} onClick={() => void reload()}><RefreshIcon className="button__icon" /> {loading ? 'Refreshing…' : 'Refresh'}</button>
            </header>
            <table className="usage-table"><thead><tr><th>Employee</th><th>Breakdown</th><th className="number">Input tokens</th><th className="number">Output tokens</th><th className="number">Total</th></tr></thead>
              <tbody>{data.items.map((item, index) => {
                const employee = item.employee_name || 'Unassigned'
                const expanded = expandedEmployeeID === item.employee_id
                const detailsID = `usage-details-${pageIndex}-${index}`
                return <Fragment key={item.employee_id}>
                  <tr className="usage-employee-row">
                    <td data-label="Employee"><button className="usage-employee-toggle" type="button" aria-expanded={expanded} aria-controls={detailsID} onClick={() => setExpandedEmployeeID(expanded ? '' : item.employee_id)}><span className="usage-employee-toggle__icon"><ChevronDownIcon /></span><strong>{employee}</strong></button></td>
                    <td data-label="Breakdown"><span className="usage-breakdown-count">{item.model_count} {item.model_count === 1 ? 'model' : 'models'}</span></td>
                    <td data-label="Input tokens" className="number">{formatTokens(item.input_tokens)}</td>
                    <td data-label="Output tokens" className="number">{formatTokens(item.output_tokens)}</td>
                    <td data-label="Total" className="number"><strong>{formatTokens(item.input_tokens + item.output_tokens)}</strong></td>
                  </tr>
                  {expanded ? <tr className="usage-detail-row"><td colSpan={5}><UsageDetails employee={employee} id={detailsID} path={usageDetailsPath(item.employee_id, range)} /></td></tr> : null}
                </Fragment>
              })}</tbody>
            </table>
            <nav className="usage-pagination" aria-label="Usage pages">
              <button className="button button--secondary" type="button" disabled={loading || pageIndex === 0} onClick={previousPage} aria-label="Previous usage page">Previous</button>
              <span aria-live="polite">Page {pageIndex + 1}</span>
              <button className="button button--secondary" type="button" disabled={loading || !data.next_cursor} onClick={nextPage} aria-label="Next usage page">Next</button>
            </nav>
          </div>
        )}
      </> : null}
    </section>
  )
}
