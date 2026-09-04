import { useEffect, useRef, type ReactNode } from 'react'
import { CloseIcon } from './Icons'

interface ModalProps {
  title: string
  description?: string
  children: ReactNode
  onClose: () => void
  closeLabel?: string
  eyebrow?: string
  className?: string
  showClose?: boolean
}

export function Modal({ title, description, children, onClose, closeLabel = 'Close dialog', eyebrow = 'Subpool console', className = '', showClose = true }: ModalProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose

  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    const initialFocus = dialogRef.current?.querySelector<HTMLElement>('[data-autofocus]')
    if (initialFocus) initialFocus.focus()
    else dialogRef.current?.focus()
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !event.defaultPrevented) onCloseRef.current()
    }
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('keydown', onKeyDown)
      previous?.focus()
    }
  }, [])

  return (
    <div className="modal-layer" role="presentation" onMouseDown={(event) => {
      if (event.currentTarget === event.target) onClose()
    }}>
      <div
        ref={dialogRef}
        className={`modal ${className}`.trim()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="modal-title"
        tabIndex={-1}
      >
        <header className="modal__header">
          <div>
            {eyebrow ? <p className="eyebrow">{eyebrow}</p> : null}
            <h2 id="modal-title">{title}</h2>
            {description ? <p>{description}</p> : null}
          </div>
          {showClose ? <button className="icon-button" type="button" aria-label={closeLabel} onClick={onClose}>
            <CloseIcon />
          </button> : null}
        </header>
        <div className="modal__body">{children}</div>
      </div>
    </div>
  )
}
