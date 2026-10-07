interface GestaCreditProps {
  className?: string
}

export function GestaCredit({ className = '' }: GestaCreditProps) {
  return (
    <a className={`sidebar__credit ${className}`.trim()} href="https://gesta.run" target="_blank" rel="noreferrer" aria-label="Supported by Gesta">
      <span>Supported by</span>
      <i aria-hidden="true" />
      <img src="/brand/gesta-icon-white.svg" alt="" />
      <strong>Gesta</strong>
    </a>
  )
}
