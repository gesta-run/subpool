import { useEffect, useRef, type ReactNode } from 'react'
import { CloseIcon } from './Icons'

interface ModalProps {
  title: string
  description?: string
  children: ReactNode
  onClose: () => void
  closeLabel?: string
}

export function Modal({ title, description, children, onClose, closeLabel = 'Close dialog' }: ModalProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose

  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    dialogRef.current?.focus()
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onCloseRef.current()
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
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="modal-title"
        tabIndex={-1}
      >
        <header className="modal__header">
          <div>
            <p className="eyebrow">Subpool console</p>
            <h2 id="modal-title">{title}</h2>
            {description ? <p>{description}</p> : null}
          </div>
          <button className="icon-button" type="button" aria-label={closeLabel} onClick={onClose}>
            <CloseIcon />
          </button>
        </header>
        <div className="modal__body">{children}</div>
      </div>
    </div>
  )
}
