interface SpinnerProps {
  className?: string
}

export function Spinner({ className = '' }: SpinnerProps) {
  return <span className={`spinner ${className}`.trim()} aria-hidden="true" />
}
