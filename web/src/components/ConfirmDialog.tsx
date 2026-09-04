import { Modal } from './Modal'

interface ConfirmDialogProps {
  title: string
  description: string
  confirmLabel: string
  variant?: 'default' | 'danger'
  onCancel: () => void
  onConfirm: () => void
}

export function ConfirmDialog({ title, description, confirmLabel, variant = 'danger', onCancel, onConfirm }: ConfirmDialogProps) {
  return (
    <Modal title={title} description={description} eyebrow="" className="modal--confirm" showClose={false} onClose={onCancel}>
      <div className="confirm-dialog__actions">
        <button className="button button--secondary" type="button" data-autofocus onClick={onCancel}>Cancel</button>
        <button className={`button ${variant === 'default' ? 'button--primary' : 'button--danger'}`} type="button" onClick={onConfirm}>{confirmLabel}</button>
      </div>
    </Modal>
  )
}
