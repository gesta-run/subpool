type SkeletonVariant = 'table' | 'list' | 'dashboard' | 'settings'

interface PageSkeletonProps {
  metrics?: 0 | 2 | 3 | 4
  variant: SkeletonVariant
}

function Line({ size = 'medium' }: { size?: 'short' | 'medium' | 'long' }) {
  return <span className={`skeleton-line skeleton-line--${size}`} />
}

export function PageSkeleton({ metrics = 0, variant }: PageSkeletonProps) {
  return (
    <div className="page-skeleton" role="status" aria-label="Loading page data">
      {metrics > 0 ? (
        <div className={`metric-strip ${metrics === 2 ? 'metric-strip--two' : metrics === 4 ? 'metric-strip--four' : ''} skeleton-metrics`}>
          {Array.from({ length: metrics }, (_, index) => <article key={index}><Line size="short" /><Line size="medium" /></article>)}
        </div>
      ) : null}
      {variant === 'table' ? (
        <div className="skeleton-table">
          <div className="skeleton-table__header">{Array.from({ length: 5 }, (_, index) => <Line key={index} size="short" />)}</div>
          {Array.from({ length: 4 }, (_, row) => <div className="skeleton-table__row" key={row}><span><Line size="medium" /><Line size="long" /></span><Line size="short" /><Line size="short" /><Line size="medium" /><Line size="short" /></div>)}
        </div>
      ) : null}
      {variant === 'list' ? <div className="skeleton-list">{Array.from({ length: 3 }, (_, index) => <div className="skeleton-list__row" key={index}>
        <span className="skeleton-list__index skeleton-line" />
        <div className="skeleton-list__main"><Line size="medium" /><Line size="long" /></div>
        <div className="skeleton-list__meta"><span><Line size="short" /><Line size="short" /></span><span><Line size="short" /><Line size="medium" /></span></div>
        <span className="skeleton-list__action skeleton-line" />
      </div>)}</div> : null}
      {variant === 'dashboard' ? <div className="skeleton-dashboard">{Array.from({ length: 2 }, (_, index) => <section key={index}><Line size="medium" /><Line size="long" />{Array.from({ length: 4 }, (_, row) => <div key={row}><Line size="medium" /><Line size="short" /></div>)}</section>)}</div> : null}
      {variant === 'settings' ? <div className="skeleton-settings"><Line size="short" /><Line size="medium" /><Line size="long" /><div className="skeleton-field" /><Line size="long" /><div className="skeleton-button" /></div> : null}
      <span className="sr-only">Loading…</span>
    </div>
  )
}

export function AppSkeleton() {
  return (
    <main className="app-skeleton" role="status" aria-label="Loading Subpool">
      <section><Line size="medium" /><div><Line size="short" /><Line size="long" /><Line size="long" /><Line size="medium" /></div></section>
      <section><div><Line size="short" /><Line size="medium" /><Line size="long" /><div className="skeleton-field" /><div className="skeleton-field" /><div className="skeleton-button" /></div></section>
      <span className="sr-only">Loading Subpool…</span>
    </main>
  )
}

export function PickerSkeleton() {
  return <div className="skeleton-picker" role="status" aria-label="Loading available accounts">{Array.from({ length: 3 }, (_, index) => <div key={index}><span className="skeleton-check" /><span><Line size="medium" /><Line size="short" /></span></div>)}<span className="sr-only">Loading available accounts…</span></div>
}
