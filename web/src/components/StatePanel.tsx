import { RefreshIcon } from './Icons'
import { Spinner } from './Spinner'

interface StatePanelProps {
  kind: 'loading' | 'empty' | 'error'
  title: string
  description: string
  actionLabel?: string
  onAction?: () => void
}

export function StatePanel({ kind, title, description, actionLabel, onAction }: StatePanelProps) {
  return (
    <div className={`state-panel state-panel--${kind}`} role={kind === 'error' ? 'alert' : 'status'}>
      <span className="state-panel__mark" aria-hidden="true">
        {kind === 'loading' ? <Spinner /> : kind === 'error' ? '!' : '0'}
      </span>
      <div>
        <h3>{title}</h3>
        <p>{description}</p>
      </div>
      {actionLabel && onAction ? (
        <button className="button button--secondary" type="button" onClick={onAction}>
          {kind === 'error' && <RefreshIcon className="button__icon" />}
          {actionLabel}
        </button>
      ) : null}
    </div>
  )
}
